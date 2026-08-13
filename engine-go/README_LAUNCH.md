# Kibborg Go Engine - Файлы запуска (Меню)

This directory contains dashboard-style launchers for the Go reference trading brain and the LLM Brain (llama-server).

## Quick Start (after models downloaded)

1. Build the Go binary (**обязательно через lint-gate**):
   ```
   build.cmd
   ```
   Порядок: `gofmt -l` → `go vet ./...` → `staticcheck ./...` → `go build`.
   Если линтер упал — `.exe` **не** пересобирается. Пункт меню «4. Собрать» и автосборка в `Start.cmd` вызывают тот же `build.cmd`.

2. Start the full stack (Brain + Trading Engine):
   ```
   start-full-stack.cmd
   ```

3. Stop:
   ```
   stop-full-stack.cmd
   ```

## Первый прогон агента

После сборки и старта — `..\ПЕРВЫЙ_ЗАПУСК.md`: пошаговая приёмка слойного агента
(измерения §3.2, маршрутизация, ворота безопасности, `/stop`, подтверждения) с командами
чтения `runtime\tasks.jsonl` и `runtime\hands.jsonl`.

## Files

- `build.cmd` - Builds the Go binary (`kibborg-go-engine.exe`).
- `start-brain.cmd` - Starts the LLM Brain (llama-server) with hardware-optimized params for Qwen3.6-35B on dual RTX 3060. Uses models in `models/brain/` and `models/vision/`.
- `start-engine.cmd` - Starts the Go trading engine (deterministic logic, port 8002).
- `start-full-stack.cmd` - Starts both (like original start-stack.cmd), prints dashboard info, opens health URLs.
- `stop-full-stack.cmd` - Stops both.
- `start.ps1` - PowerShell alternative with parameters.
- `download-models.ps1` - Downloads the recommended models using huggingface-cli (run this first if models not present).
- `SETUP_HARDWARE.md` - Full hardware setup instructions (copied from your spec).

## Hardware Notes (for 2x RTX 3060 12GB)

See `SETUP_HARDWARE.md` for the full block with model recommendations (Qwen3.6-35B-A3B UD-IQ4_XS recommended), download commands, and optimized llama-server command.

The `start-brain.cmd` uses the dual-GPU settings:
- --tensor-split 0.35,0.65
- --n-gpu-layers 99
- --ctx-size 32768
- etc.

Monitor with `nvidia-smi -l 1`.

If OOM, reduce layers or ctx size.

## Integration

- Brain (LLM + Vision): http://127.0.0.1:8083 (for the bot's LLM calls)
- Trading Engine (Go): http://127.0.0.1:8002 (for deterministic tools like full_trading_check)

The Go engine is the reference implementation of the old Python trading brain - LLM never invents numbers.

Run the download first if needed:
powershell -File download-models.ps1

Then start-full-stack.cmd

**KIBBORG in Go is ready!**
