# Kibborg Go Engine (migration in progress)

This is the start of the Go port of the deterministic trading brain and tools server.

## Why
- Remove Python process overhead for the part that must never hallucinate numbers.
- Better performance, typing, and concurrency for the heavy trading math.
- Single (or small number of) static binaries instead of multiple venvs.

## Current status
- Stub HTTP server on :8002
- `/health`
- `/full_trading_check` (returns a minimal DecisionReport stub)
- Core schema ported (`trading/schema.go`)
- Minimal brain stub (`trading/brain.go`)

## How to run (during development)
```bash
cd engine-go
go run main.go
```

The Python bot can point `COMPUTER_TOOLS_URL=http://127.0.0.1:8002` (and adjust key if needed) to test the Go side while the real Python engine stays on 8001.

## Migration order (see PYTHON_TO_GO_MIGRATION_PLAN.md)
1. Trading brain + DecisionReport (this)
2. Rest of computer-tools tools surface
3. (much later) bot orchestration if desired

## Next concrete steps
- Port regime_classifier, scoring, risk, discipline, confidence calibration from Python.
- Add real data sources (Binance candles, etc.).
- Make full_trading_check and other tools return 100% identical output to the Python version (characterization).
- Add proper auth (same Bearer key as Python engine).

All numbers that ever reach the LLM must still come from this (Go) code.
