# Python → Go Migration Plan for Kibborg_DaVinchi_Bot

**Goal:** Increase long-term stability, performance, binary size, and concurrency characteristics while preserving the most important invariants of the system.

**Core Invariant (non-negotiable):**
> The LLM (any model) **never invents numbers**. All prices, scores, risk, probability, discipline decisions, confidence, etc. must come from deterministic code. The "trading brain" is the single source of truth.

## Why consider Go?

Current strengths of the Python implementation:
- Extremely rich ecosystem for the Telegram bot layer (python-telegram-bot, httpx, etc.).
- Very sophisticated layered prompt system + agent runner + skills.
- Huge amount of tested trading mathematics (15+ specialized modules).
- Characterization tests that give very strong behavioral guarantees.

Pain points that Go can address:
- Python GIL + process-per-organ model (many venvs: .venv, .venv-embeddings, .venv-stt, .venv-tts, telegram .venv).
- Heavy startup and memory overhead for several Python interpreters.
- Tool execution and browser control (Playwright) can be faster/more reliable in a single Go binary.
- Easier to produce a single small static binary for the engine + tools server.
- Better structured concurrency for agent loops and watchdogs.
- Simpler deployment (one binary per major component instead of 5+ venvs + python).

**Recommendation:** Do **not** do a big-bang rewrite. Use a phased, boundary-respecting approach.

## Recommended Migration Phases (high to low value / low to high risk)

### Phase 0 — Preparation (do this first, language-agnostic)
- Freeze and document the public contracts:
  - `computer-tools` HTTP API (all the trading tools + browser + fs + vision_extract).
  - DecisionReport schema (`trading/schema.py` and friends).
  - Tool call format (the JSON the model is supposed to emit).
- Strengthen the test harness around the contracts (characterization + property-based tests on DecisionReport).
- Extract the pure trading logic even more cleanly (already quite good in `trading/brain.py` + modules).
- Make sure the current Python version is rock-solid (the work we just did on watchdog + parser helps).

### Phase 1 — Trading Brain + Core Deterministic Engine (highest ROI, lowest risk)
**Target:** Port `computer-tools/trading/` (and the parts of `server.py` that call it) to Go.

- This is almost pure computation + some data fetching (Binance REST + WS for orderflow etc.).
- No LLM, no Telegram.
- Can be exposed as a Go HTTP server (or gRPC) that the rest of the system calls exactly like today.
- Enormous win for stability and speed of the part that must **never lie** about numbers.
- The existing Python `brain.py` aggregator can stay as a thin caller during transition (or be deleted once the Go version is proven).

**Success criteria:**
- 100% of current `test_trading_*.py` + `test_walkforward.py` etc. pass against the Go implementation (via the HTTP boundary or by porting tests).
- Identical `DecisionReport` JSON for the same inputs (characterization).
- Same or better latency on heavy checks (`full_trading_check`).

**Risk:** Low if we keep the same mathematical modules (regime, scoring, discipline, confidence calibration, adilbot_strategy, etc.).

### Phase 2 — Computer Tools Server (the "organs" + tools surface)
**Target:** Rewrite (or heavily replace) `computer-tools/server.py` + `server_core.py` in Go.

Components to port:
- Tool registry / execution router
- Browser control (Playwright → rod or chromedp or go-rod)
- Desktop control (robotgo or similar, or keep some Python bridge if too painful on Windows)
- OCR (keep Tesseract via cgo or external, or switch to a Go-friendly engine)
- RAG / embeddings client (call the existing BGE-M3 server or embed a small Go embedding client)
- Vision extract legacy path (optional)
- Health/full health endpoints
- Task manager if still used

**Boundary:** The bot (Python or future Go bot) talks to this server over HTTP exactly as today (`COMPUTER_TOOLS_URL` + Bearer key).

This phase gives a single Go binary for almost everything deterministic + tools.

### Phase 3 — Bot Orchestration Layer (the hard part)
**Target:** The `telegram-assistant/kibborg/` package (or a large part of it).

This includes:
- Telegram handlers (messages, commands, voice, photos)
- Layered prompt builder (CORE, STYLE, ROLE, TOOLS, ADILBOT, SECURITY) — this is very sophisticated and language-specific in its string manipulation.
- Intent classifier (deterministic — easy to port)
- Agent runner + LoopGuard + traces
- Skills system (TOML loader + executor)
- Memory (dual json + qdrant)
- Watch scheduler
- Tool parser + danger gating + executor (the executor can call the Go computer-tools)
- Voice I/O glue
- Render layer (very Telegram-specific Markdown + blocks)

**Options:**
A. Keep the bot in Python for a long time (recommended initially). It is the "glue" and user-facing part. Python excels here.
B. Port only the orchestration skeleton to Go (using a good Telegram library like `telegram-bot-api` or `go-telegram-bot-api`) and keep prompt building + skills in Python (via subprocess or a small Python sidecar).
C. Full port later, once the engine is proven in Go.

**Risk:** High. The prompt layering, characterization tests, and "assistant-first" philosophy are deeply baked into the Python code and tests. A bad port here can destroy the "personality" and reliability the users rely on.

### Phase 4 — AdilBot copilot loops + ancillary services
- `adilbot_bridge.py`, `adilbot_suggest_loop.py`
- These are relatively small autonomous loops. Can be rewritten in Go once the core trading client is stable.

## Architecture During Transition (recommended)

```
Telegram
   │
   ▼
Python Bot (kibborg/)   ← keep for longest time
   │  (llm_chat + prompt layers + agent + skills + render)
   │
   ├──► Brain (llama.cpp) :8080   (never port this)
   │
   └──► Computer-Tools (Go in Phase 1+2) :8001
             │
             ├── Trading Brain (Go)
             ├── Browser / FS / Desktop / OCR
             └── RAG client → existing embeddings server
```

- Use the existing HTTP contracts as the stable seam.
- Python and Go can run side-by-side for years.
- The Python side only needs to know how to call the Go server and how to call the LLM.

## State & Persistence

- Journal, approvals, memory (json fallback), traces.sqlite — Go can use the same SQLite + files + talk to Qdrant.
- For the embedded Qdrant case: either keep the Python memory process or switch to a small Go Qdrant client + embedded mode if available.

## Testing Strategy (critical for trust)

1. Keep the existing `test_characterization.py` (byte-for-byte) as the golden master.
2. When porting a module, run the Python version and the Go version on the same inputs and diff the DecisionReport (or full tool output).
3. Port the property-based / walkforward / regime tests early.
4. For the bot layer: use the existing integration + prompt tests as regression.

## Risks & Mitigations

- **Loss of "never invent numbers"** → Make the Go trading brain the *only* producer of DecisionReport. The LLM side only ever receives and renders it.
- **Behavioral drift in prompts / agent** → Keep Python bot layer longer. Characterization tests must stay green.
- **Windows desktop/browser control** → This is the ugliest part in Go on Windows. Evaluate `rod` + `robotgo` or keep a small Python "desktop tools" helper process if needed.
- **Team knowledge** → The trading math is complex. Porting it requires the person who understands `regime_classifier + discipline + confidence + adilbot_strategy` to be involved.
- **llama.cpp integration** — keep it external over HTTP. No need to embed in Go.

## Suggested Order (pragmatic)

1. Phase 0 (prep + tests) — 2-4 weeks
2. Phase 1 (trading brain in Go + HTTP server that returns identical DecisionReport) — biggest stability win
3. Make the current Python computer-tools server a thin proxy / compatibility layer or delete it
4. Phase 2 (full tools server in Go)
5. Keep Python bot for 6-18 months (or forever for the complex conversational part)
6. Only then consider Phase 3 (bot) if there is a strong reason (single binary, lower resource usage on the machine, etc.)

## What Probably Stays in Python (or a small Python service) for a long time

- The full prompt layering and role system (extremely tuned for the small local model)
- Skills (TOML-defined, easy to evolve)
- Agent traces + LoopGuard + planner (high-level orchestration logic)
- Telegram message formatting and voice glue
- Ad-hoc user memory and watch scheduler

## Deliverables of a Successful Migration

- A single (or two) small Go binaries that replace the current 5+ Python processes for the engine side.
- The same (or better) `stack.ps1` / dashboard experience.
- All existing characterization + trading tests still pass.
- The "LLM never invents numbers" property is easier to audit (because the Go code that produces numbers is smaller and strongly typed).

## Estimated Difficulty

| Component              | Difficulty | Time (rough) | Value |
|------------------------|------------|--------------|-------|
| Trading brain          | Medium     | 4-8 weeks    | Very High |
| Full computer-tools    | Medium-High| 6-12 weeks   | High |
| Bot orchestration      | Very High  | 4-8+ months  | Medium (if Python bot kept) |
| Full "one Go binary"   | Extreme    | 9-18 months  | Nice to have |

**Bottom line:** Port the **engine** (trading + tools) to Go first. Keep the sophisticated conversational bot in Python for as long as it remains the most productive place to evolve the "personality" and agent behavior.

This gives most of the stability and operational wins with the least risk of breaking what makes Kibborg special.

---

*Written after the 2026-06 stability hardening pass (auto-watchdog + tool-call retry + legacy cleanup).*
