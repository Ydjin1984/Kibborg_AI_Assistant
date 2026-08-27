#!/usr/bin/env python3
"""Локальный TTS-сервер: Qwen3-TTS 0.6B через faster-qwen3-tts (CUDA Graphs).

Весь decode идёт на GPU одним графом — без пошагового дёрганья с CPU.
Контракт Kibborg: POST /v1/tts → WAV, GET /v1/health.
"""

from __future__ import annotations

import argparse
import io
import json
import logging
import os
import sys
import threading
import time
import traceback
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any

import numpy as np
import soundfile as sf
import torch

log = logging.getLogger("qwen-tts")

MODEL_ID_DEFAULT = "Qwen/Qwen3-TTS-12Hz-0.6B-CustomVoice"
DEFAULT_VOICE = "Serena"

VOICE_ALIASES = {
    "f1": "Serena",
    "f2": "Vivian",
    "f3": "Serena",
    "f4": "Vivian",
    "f5": "Serena",
    "m1": "Ryan",
    "m2": "Aiden",
    "m3": "Dylan",
    "m4": "Eric",
    "m5": "Uncle_Fu",
}

LANG_MAP = {
    "ru": "Russian",
    "russian": "Russian",
    "en": "English",
    "english": "English",
    "auto": "Auto",
    "na": "Auto",
    "": "Auto",
}

_model = None
_model_lock = threading.Lock()
_ready = False
_cfg: dict[str, Any] = {}


def _resolve_voice(raw: str) -> str:
    v = (raw or "").strip()
    if not v:
        return _cfg.get("voice") or DEFAULT_VOICE
    key = v.lower().replace("-", "_")
    if key in VOICE_ALIASES:
        return VOICE_ALIASES[key]
    known = {
        "vivian": "Vivian",
        "serena": "Serena",
        "uncle_fu": "Uncle_Fu",
        "dylan": "Dylan",
        "eric": "Eric",
        "ryan": "Ryan",
        "aiden": "Aiden",
        "ono_anna": "Ono_Anna",
        "sohee": "Sohee",
    }
    return known.get(key, v)


def _resolve_lang(raw: str) -> str:
    key = (raw or "").strip().lower()
    return LANG_MAP.get(key, raw.strip() if raw and raw.strip() else "Auto")


def load_model(model_id: str) -> None:
    global _model, _ready
    from faster_qwen3_tts import FasterQwen3TTS

    if not torch.cuda.is_available():
        raise RuntimeError("CUDA недоступна — озвучка только на GPU")

    device = "cuda:0"  # после CUDA_VISIBLE_DEVICES это нужная карта
    dtype = torch.bfloat16
    log.info("гружу FasterQwen3TTS %s на %s dtype=%s (CUDA Graphs)", model_id, device, dtype)
    log.info(
        "GPU: %s | free≈%.1f GiB",
        torch.cuda.get_device_name(0),
        torch.cuda.mem_get_info(0)[0] / (1024**3),
    )

    _model = FasterQwen3TTS.from_pretrained(
        model_id,
        device=device,
        dtype=dtype,
        attn_implementation="sdpa",
    )
    # Прогрев: захват CUDA graph, чтобы первый реальный запрос не ждал compile.
    voice = _resolve_voice(_cfg.get("voice") or DEFAULT_VOICE)
    try:
        if hasattr(_model, "warmup"):
            _model.warmup(prefill_len=100)
            log.info("warmup CUDA Graphs OK")
        wavs, sr = _model.generate_custom_voice(
            text="Привет.",
            language="Russian",
            speaker=voice,
        )
        del wavs, sr
        torch.cuda.synchronize()
        log.info("прогрев синтеза OK")
    except Exception as e:  # noqa: BLE001
        log.warning("прогрев: %s", e)

    _ready = True
    used = torch.cuda.memory_allocated(0) / (1024**3)
    log.info("модель готова на GPU, занято ≈%.2f GiB VRAM", used)


def synthesize(text: str, voice: str, lang: str) -> bytes:
    if _model is None:
        raise RuntimeError("модель ещё не загружена")
    speaker = _resolve_voice(voice)
    language = _resolve_lang(lang)
    t0 = time.perf_counter()
    with _model_lock:
        with torch.inference_mode():
            wavs, sr = _model.generate_custom_voice(
                text=text,
                language=language,
                speaker=speaker,
            )
            torch.cuda.synchronize()
    elapsed = time.perf_counter() - t0
    audio = np.asarray(wavs[0], dtype=np.float32)
    dur = float(len(audio)) / float(sr) if sr else 0.0
    rtf = (dur / elapsed) if elapsed > 0 else 0.0
    log.info(
        "synth %.2fs audio=%.2fs RTF=%.2fx voice=%s lang=%s chars=%d",
        elapsed,
        dur,
        rtf,
        speaker,
        language,
        len(text),
    )
    buf = io.BytesIO()
    sf.write(buf, audio, int(sr), format="WAV", subtype="PCM_16")
    return buf.getvalue()


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt: str, *args: Any) -> None:
        log.info("%s - %s", self.address_string(), fmt % args)

    def _json(self, code: int, obj: dict[str, Any]) -> None:
        body = json.dumps(obj, ensure_ascii=False).encode("utf-8")
        self.send_response(code)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _raw(self, code: int, data: bytes, content_type: str) -> None:
        self.send_response(code)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self) -> None:  # noqa: N802
        if self.path.split("?", 1)[0] in ("/v1/health", "/health", "/"):
            gpu = torch.cuda.get_device_name(0) if torch.cuda.is_available() else ""
            vram = ""
            if torch.cuda.is_available():
                free, total = torch.cuda.mem_get_info(0)
                vram = f"{(total - free) / (1024**3):.2f}/{total / (1024**3):.1f} GiB"
            self._json(
                200,
                {
                    "status": "ok" if _ready else "loading",
                    "ready": _ready,
                    "engine": "faster-qwen3-tts",
                    "backend": "cuda-graphs",
                    "model": _cfg.get("model"),
                    "voice": _cfg.get("voice"),
                    "gpu": gpu,
                    "vram": vram,
                },
            )
            return
        self._json(404, {"error": "not found"})

    def do_POST(self) -> None:  # noqa: N802
        path = self.path.split("?", 1)[0]
        if path not in ("/v1/tts", "/tts"):
            self._json(404, {"error": "not found"})
            return
        if not _ready:
            self._json(503, {"error": "модель ещё грузится"})
            return
        try:
            n = int(self.headers.get("Content-Length", "0"))
            raw = self.rfile.read(n) if n > 0 else b"{}"
            req = json.loads(raw.decode("utf-8") if raw else "{}")
        except Exception as e:  # noqa: BLE001
            self._json(400, {"error": f"плохой JSON: {e}"})
            return
        text = (req.get("text") or "").strip()
        if not text:
            self._json(400, {"error": "нужно поле text"})
            return
        voice = str(req.get("voice") or "")
        lang = str(req.get("lang") or req.get("language") or "Auto")
        try:
            wav = synthesize(text, voice, lang)
        except Exception as e:  # noqa: BLE001
            log.error("синтез упал: %s\n%s", e, traceback.format_exc())
            self._json(500, {"error": str(e)})
            return
        self._raw(200, wav, "audio/wav")


def main() -> int:
    ap = argparse.ArgumentParser(description="Faster Qwen3-TTS (CUDA Graphs) for Kibborg")
    ap.add_argument("--host", default="127.0.0.1")
    ap.add_argument("--port", type=int, default=7788)
    ap.add_argument("--model", default=os.environ.get("TTS_MODEL", MODEL_ID_DEFAULT))
    ap.add_argument("--voice", default=os.environ.get("TTS_VOICE", DEFAULT_VOICE))
    args = ap.parse_args()

    logging.basicConfig(
        level=logging.INFO,
        format="[TTS] %(asctime)s %(levelname)s %(message)s",
        datefmt="%H:%M:%S",
    )
    _cfg["model"] = args.model
    _cfg["voice"] = args.voice

    if not torch.cuda.is_available():
        log.error("нет CUDA — отказ. Озвучка только GPU.")
        return 2

    httpd = ThreadingHTTPServer((args.host, args.port), Handler)
    httpd.daemon_threads = True
    log.info(
        "слушаю http://%s:%d  model=%s voice=%s backend=cuda-graphs",
        args.host,
        args.port,
        args.model,
        args.voice,
    )

    def _boot() -> None:
        try:
            load_model(args.model)
        except Exception:
            log.error("не смог загрузить модель:\n%s", traceback.format_exc())
            os._exit(2)

    threading.Thread(target=_boot, name="tts-load", daemon=True).start()
    try:
        httpd.serve_forever()
    except KeyboardInterrupt:
        log.info("стоп")
    return 0


if __name__ == "__main__":
    sys.exit(main())
