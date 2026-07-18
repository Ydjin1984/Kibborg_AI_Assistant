package main

// Voice messages: Telegram voice/audio → ffmpeg (opus → 16kHz mono wav) → whisper.cpp
// server /inference → text → the normal chat pipeline. The whisper server is launched in
// the background at startup (like the brain) when WHISPER_SERVER + WHISPER_MODEL are set
// in settings.ini; without them voice replies with setup instructions instead of failing.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const voiceTmpDir = "runtime/voice"

// maxVoiceSeconds keeps CPU transcription snappy; longer clips are refused politely.
const maxVoiceSeconds = 600

// voiceEnabled reports whether speech recognition is configured.
func voiceEnabled(cfg Config) bool {
	return cfg.WhisperExe != "" && cfg.WhisperModel != ""
}

// ensureWhisper launches whisper.cpp's server unless the port is already taken.
// Mirrors ensureBrain: check the listener, not an HTTP health endpoint.
func ensureWhisper(cfg Config) {
	if !voiceEnabled(cfg) {
		log.Printf("[VOICE] WHISPER_SERVER/WHISPER_MODEL не заданы — голосовые выключены")
		return
	}
	if portInUse(cfg.WhisperPort) {
		log.Printf("[VOICE] whisper port :%d already in use — not launching a second instance", cfg.WhisperPort)
		return
	}
	if _, err := os.Stat(cfg.WhisperExe); err != nil {
		log.Printf("[VOICE] whisper-server not found: %s", cfg.WhisperExe)
		return
	}
	if _, err := os.Stat(cfg.WhisperModel); err != nil {
		log.Printf("[VOICE] whisper model not found: %s", cfg.WhisperModel)
		return
	}
	cmd := exec.Command(cfg.WhisperExe,
		"-m", cfg.WhisperModel,
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(cfg.WhisperPort),
		"-l", "auto",
	)
	cmd.Dir = filepath.Dir(cfg.WhisperExe) // load its DLLs from the build dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		log.Printf("[VOICE] failed to launch whisper-server: %v", err)
		return
	}
	registerEngineProc(cmd.Process)
	log.Printf("[VOICE] whisper-server launched (pid %d) :%d — %s",
		cmd.Process.Pid, cfg.WhisperPort, filepath.Base(cfg.WhisperModel))
}

// ffmpegExe resolves the ffmpeg binary: explicit FFMPEG setting wins, else PATH.
func ffmpegExe(cfg Config) string {
	if cfg.FfmpegPath != "" {
		return cfg.FfmpegPath
	}
	return "ffmpeg"
}

// transcribeAudio converts arbitrary Telegram audio bytes to 16kHz mono WAV and sends them
// to the whisper server, returning the recognized text.
func transcribeAudio(cfg Config, data []byte, srcExt string) (string, error) {
	if err := os.MkdirAll(voiceTmpDir, 0o755); err != nil {
		return "", err
	}
	stamp := fmt.Sprintf("%d", time.Now().UnixNano())
	src := filepath.Join(voiceTmpDir, "in-"+stamp+srcExt)
	wav := filepath.Join(voiceTmpDir, "out-"+stamp+".wav")
	if err := os.WriteFile(src, data, 0o644); err != nil {
		return "", err
	}
	defer os.Remove(src)
	defer os.Remove(wav)

	// Whisper wants 16kHz mono PCM; Telegram voice is OGG/Opus.
	conv := exec.Command(ffmpegExe(cfg), "-y", "-i", src, "-ar", "16000", "-ac", "1", "-f", "wav", wav)
	if out, err := conv.CombinedOutput(); err != nil {
		return "", fmt.Errorf("ffmpeg не сконвертировал аудио: %v (%s)", err, capLogTail(string(out)))
	}

	wavData, err := os.ReadFile(wav)
	if err != nil {
		return "", err
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", "audio.wav")
	if err != nil {
		return "", err
	}
	if _, err := part.Write(wavData); err != nil {
		return "", err
	}
	_ = mw.WriteField("response_format", "json")
	_ = mw.WriteField("temperature", "0.0")
	mw.Close()

	c := &http.Client{Timeout: 5 * time.Minute}
	resp, err := c.Post(
		fmt.Sprintf("http://127.0.0.1:%d/inference", cfg.WhisperPort),
		mw.FormDataContentType(), &body)
	if err != nil {
		return "", fmt.Errorf("whisper-server недоступен: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("whisper HTTP %d: %s", resp.StatusCode, capLogTail(string(raw)))
	}
	var parsed struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("whisper вернул не-JSON: %s", capLogTail(string(raw)))
	}
	text := strings.TrimSpace(parsed.Text)
	if text == "" {
		return "", fmt.Errorf("речь не распознана (пустой результат)")
	}
	return text, nil
}

// handleVoiceMessage downloads the voice/audio, transcribes it, echoes the transcript and
// answers it through the normal streaming chat pipeline.
func handleVoiceMessage(cfg Config, botAPI string, chatID int64, fileID string, duration int, mimeType string) {
	if !voiceEnabled(cfg) {
		sendTelegramMessage(botAPI, chatID,
			"🎙 Голосовые пока выключены. Чтобы включить:\n"+
				"1. Скачай сборку whisper.cpp (whisper-server.exe) и ggml-модель (например `ggml-large-v3-turbo.bin`).\n"+
				"2. В `settings.ini` заполни `WHISPER_SERVER=` и `WHISPER_MODEL=` (модель клади в `models\\whisper`).\n"+
				"3. Перезапусти бота.")
		return
	}
	if duration > maxVoiceSeconds {
		sendTelegramMessage(botAPI, chatID, fmt.Sprintf("🎙 Слишком длинное аудио (%d мин) — пришли до %d минут.", duration/60, maxVoiceSeconds/60))
		return
	}
	stop := startTyping(botAPI, chatID)
	defer stop()

	filePath, err := getTelegramFilePath(botAPI, fileID)
	if err != nil {
		sendTelegramMessage(botAPI, chatID, "❌ Не смог получить аудио из Telegram: "+err.Error())
		return
	}
	data, _, err := downloadTelegramFile(cfg.TelegramToken, filePath)
	if err != nil {
		sendTelegramMessage(botAPI, chatID, "❌ Не смог скачать аудио: "+err.Error())
		return
	}
	ext := audioExt(filePath, mimeType)
	text, err := transcribeAudio(cfg, data, ext)
	if err != nil {
		log.Printf("[VOICE] transcribe error: %v", err)
		sendTelegramMessage(botAPI, chatID, "❌ Не смог распознать речь: "+err.Error())
		return
	}
	log.Printf("[VOICE] transcribed %d chars from %d", len(text), chatID)
	sendTelegramMessage(botAPI, chatID, "🎙 Распознал: «"+text+"»")

	if !brainReady(cfg.BrainPort) {
		sendTelegramMessage(botAPI, chatID, "⏳ Модель ещё грузится — отвечу на текст выше, когда будет готова. Повтори чуть позже.")
		return
	}
	chatWithHistoryStream(cfg, botAPI, chatID, text, stop)
}

// audioExt picks a sensible file extension for ffmpeg input detection.
func audioExt(filePath, mimeType string) string {
	if ext := strings.ToLower(filepath.Ext(filePath)); ext != "" {
		return ext
	}
	switch {
	case strings.Contains(mimeType, "ogg"), strings.Contains(mimeType, "opus"):
		return ".oga"
	case strings.Contains(mimeType, "mpeg"), strings.Contains(mimeType, "mp3"):
		return ".mp3"
	case strings.Contains(mimeType, "mp4"), strings.Contains(mimeType, "m4a"):
		return ".m4a"
	case strings.Contains(mimeType, "wav"):
		return ".wav"
	default:
		return ".oga"
	}
}

// capLogTail keeps error payloads (ffmpeg/whisper output) short enough for Telegram/logs.
// The tail matters more than the head: ffmpeg prints the actual error last.
func capLogTail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= 300 {
		return s
	}
	tail := s[len(s)-300:]
	for len(tail) > 0 && !utf8.RuneStart(tail[0]) {
		tail = tail[1:]
	}
	return "…" + tail
}
