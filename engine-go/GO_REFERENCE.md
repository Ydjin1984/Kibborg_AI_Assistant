# KIBBORG GO — Canonical Reference Implementation

**Status:** Active reference / migration target (as of 2026-06 stability pass + Go port start).

This document is the **single source of truth** for the Go version of Kibborg. 
The Python code (telegram-assistant + computer-tools) is considered the legacy implementation. 
All future development, new features, and the final production deployment should treat the Go engine as the authoritative version.

## Core Philosophy & Non-Negotiable Invariant

> **The LLM (the "Brain") NEVER invents numbers.**

All prices, scores, risk levels, probabilities, confidence values, regime classifications, position sizing, discipline decisions, plans, etc. **must** be produced by deterministic code in this Go module (or sub-packages it calls).

The LLM's only jobs are:
- Intent routing / clarification
- Narrative / explanation of results that were computed here
- Tool selection (when to call the engine)

This invariant was the foundation of the original Python system and is **preserved exactly** in the Go port.

## High-Level Architecture (Go as the Future)

```
Telegram User
      │
      ▼
[Python Bot Layer — temporary glue during migration]
      │   (prompt building, agent runner, skills, render, Telegram I/O, memory)
      │
      ├─► Brain (llama.cpp / Qwen-VL)  :8080          (unchanged — external)
      │
      └─► Kibborg Go Engine            :8002 (or 8001 in prod)
            │
            ├── trading/          (the sacred deterministic brain)
            │     ├── schema.go   (DecisionReport — the only thing the LLM may see for numbers)
            │     ├── regime.go
            │     ├── scoring.go
            │     ├── brain.go    (aggregator)
            │     └── ... (risk, discipline, confidence, plan, probability, etc.)
            │
            ├── browser/          (Browser Agent — CDP via chromedp; DOM/network/clone/screenshot tools)
            │
            ├── tools/            (fs, desktop, OCR, web, execution — future)
            │
            └── server/ (HTTP API — same contract the Python bot already uses)
```

**Boundaries (stable during migration):**
- The Go engine speaks the same HTTP API as the old `computer-tools` server.
- Python bot can be pointed at the Go engine via `COMPUTER_TOOLS_URL` (with the same Bearer key).
- The LLM brain stays exactly as-is (llama.cpp + mmproj).
- State (journal, memory, traces, approvals) can be shared or gradually moved.

## Package Structure (Reference)

```
engine-go/
├── go.mod
├── main.go                 # HTTP server (health, full_trading_check, ticker_analysis, ...)
├── trading/
│   ├── schema.go           # DecisionReport, ScoreBreakdown, etc. (canonical structs)
│   ├── regime.go           # ClassifyRegime (full port of Python logic)
│   ├── scoring.go          # ScoreLong / ScoreShort + components (ported)
│   ├── brain.go            # AnalyzeSymbol — the main aggregator
│   └── [future] risk.go, discipline.go, confidence.go, plan.go, probability.go ...
├── tools/
│   └── [future] browser.go, execution.go, rag.go ...
└── GO_REFERENCE.md         # THIS FILE — the living spec
```

## Key Data Model — DecisionReport (the contract)

All quantitative output flows through `trading.DecisionReport` (and its nested structs).

This is a direct, faithful port of the Python `computer-tools/trading/schema.py` + `ScoreBreakdown` etc.

When the LLM receives a result, it receives a `DecisionReport` (or a rendered view of it). It is forbidden to modify the numeric fields.

Example (JSON over HTTP):

```json
{
  "symbol": "BTCUSDT",
  "market": "futures",
  "direction": "long",
  "final_score": 78.4,
  "regime": "trend_up",
  "confidence": 0.81,
  "probability": 0.73,
  "risk": { ... },
  "plan": { ... },
  "discipline_guard": { "status": "ALLOW", ... },
  "context_flags": [...],
  "meta": {
    "engine": "kibborg-go",
    "invariant": "LLM never invents numbers - all values from this Go module"
  }
}
```

## Current Implementation Status (Reference Build) - 2026-06

The Go engine is now a **buildable, functional reference** for the core deterministic trading brain.

- **schema.go**: Full DecisionReport, ScoreComponent, ScoreBreakdown (single source of truth).
- **regime.go**: Complete port of classify_regime (panic/squeeze/volatile/trend_up/trend_down/range/transition + all flags, metrics, reasons).
- **scoring.go**: ScoreLong / ScoreShort with trend_alignment, momentum, volume (structure/plan stubs; matches Python behavior).
- **brain.go**: AnalyzeSymbol aggregator that combines regime + scoring + basic confidence/probability and returns a full DecisionReport.
- **main.go**: Working HTTP server on :8002 with:
  - GET /health
  - POST /full_trading_check (accepts timeframes/contexts, returns real DecisionReport from Go logic)
  - GET /ticker_analysis
  - GET /tools
- Demo data + example JSON in examples/ so you can immediately test end-to-end.
- go build succeeds cleanly.

This is the future canonical implementation. The Python trading brain is now legacy. All new deterministic trading math goes here.

**How to use as reference during migration:**
1. go build -o kibborg-go-engine .
2. ./kibborg-go-engine   (runs on :8002)
3. Point a test bot with COMPUTER_TOOLS_URL=http://127.0.0.1:8002 (and matching key) and call full_trading_check or ticker_analysis.
4. Compare DecisionReport output to the old Python version (they should be semantically identical for the same input timeframes).

The GO_REFERENCE.md + the code in trading/ is the living spec. Extend by porting more modules (risk, discipline, plan, etc.) here first.

## How to Use During Migration (Practical)

1. Build & run the Go engine (separate port during transition):
   ```bash
   cd engine-go
   go run main.go
   ```

2. Point a test bot / .env at it:
   ```
   COMPUTER_TOOLS_URL=http://127.0.0.1:8002
   ```

3. Compare outputs:
   - Call the same symbol with identical timeframe data on both Python (8001) and Go (8002).
   - The `final_score`, `regime`, `confidence`, etc. should be semantically equivalent (small floating point differences ok).

4. When confidence is high, flip the default in the stack / .env to the Go engine.

## Invariants & Rules for Contributors (Go side)

- Every number that could ever be shown to a user or fed to the LLM **must** be computed inside `trading/` (or a package it imports).
- No `math/rand` for trading decisions without an explicit audit comment.
- All public endpoints must be able to return a `DecisionReport` (or a slice of them).
- When porting a new Python module, the Go version + the Python version must produce matching results on the same inputs (use the existing characterization tests as the oracle).
- The Go engine must remain stateless with respect to user intent — it only receives data + parameters.

## Future Roadmap (inside this reference)

1. Complete port of the rest of the trading modules (risk, discipline, plan, probability, freshness, exchange_health, replay, portfolio, walkforward, adilbot_strategy, consensus, etc.).
2. Real data layer (Binance REST + WebSocket clients in Go).
3. Full tool surface: **browser via chromedp — DONE** (`browser/`, see `browser/BROWSER_AGENT.md`); still to do: desktop automation, FS, OCR via tesseract or pure-Go alternative, RAG client.
4. Auth, rate limiting, structured logging, Prometheus metrics.
5. Make the Go binary the default in `stack.ps1` / start scripts.
6. (Much later) Evaluate moving parts of the bot orchestration here if the wins justify the cost (prompt building will probably stay in Python or a small dedicated service for a long time).

## Relationship to the Original Python Code

- The Python `computer-tools/trading/` and `server.py` are now "reference implementation v1".
- This Go code is "v2" / canonical.
- The Python bot layer (kibborg/) will remain the user-facing piece for the foreseeable future (it excels at the complex conversational + agent + Telegram glue).
- The Go engine is the part we trust with money and risk calculations.

## Building a Reference-Grade Port

When adding new logic:
- Start from the Python source as the spec.
- Write the Go version.
- Add (or reuse) test vectors that the Python characterization tests already use.
- Update this document.

This file (`GO_REFERENCE.md`) + the code in `engine-go/trading/` together form the living specification that the rest of the system (Python or future pure-Go bot) will rely on.

---

**This document was created as the permanent future reference for the Go version of Kibborg during the 2026-06 migration effort.**

All subsequent work on the deterministic engine should be done here first, then (if needed) mirrored or called from other languages.

LLM never invents numbers. The Go engine is where the numbers come from.
