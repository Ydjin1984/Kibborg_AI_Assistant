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
      ├─► Brain (llama.cpp / Qwen-VL)  :8083          (unchanged — external)
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
├── main.go                 # Telegram polling, routing, /stop + /hands ДО очереди
├── agent_loop.go           # слои 2–4 слойного агента: сбор рук, исполнение, ответ
├── dispatcher.go           # слой 1: JSON-план {packs, plan, confirm, summary}
├── packs.go                # паки инструментов + request_pack + бюджет схем
├── guard.go                # ворота: Decision{Action, Risk, Rule, Reason}
├── task.go                 # Task/TaskStatus/реестр chatID→activeTask/журналы
├── pending.go              # неблокирующие подтверждения (метаданные на диск, шаги в RAM)
├── handsmode.go            # рубильник safe/full (runtime-store, не settings.ini)
├── toolresult.go           # ToolResult{Text, Status, Artifacts, Err}
├── tools_local.go          # схемы паков trade / secops
├── system_tools.go         # пак system: экран, окна, клавиатура, мышь, процессы, буфер
├── desktop_windows.go      # WinAPI под пак system (GDI, SendInput, EnumWindows)
├── desktop_other.go        # честные заглушки того же для не-Windows
├── video.go                # слой видео: ffprobe/ffmpeg, речь кусками, кадры, свёртка, конвертация
├── video_tools.go          # пак media со стороны main: analyze_video и соседи
├── video_ingest.go         # ролик, присланный в чат: Telegram и Web одной трубой
├── referent.go             # слой 0: «указание без предмета» → переспросить, а не угадывать
├── compact.go              # /compact, автосжатие истории, учёт окна контекста
├── telegram_menu.go        # setMyCommands + инлайн-панель /menu + callback_query
├── jsonl.go                # hands.jsonl / tasks.jsonl + ротация
├── trading/
│   ├── schema.go           # DecisionReport, ScoreBreakdown, etc. (canonical structs)
│   ├── regime.go           # ClassifyRegime (full port of Python logic)
│   ├── scoring.go          # ScoreLong / ScoreShort + components (ported)
│   ├── brain.go            # AnalyzeSymbol — the main aggregator
│   └── risk.go, plan.go, confidence.go, sizing.go, indicators.go
├── browser/                # Chrome CDP + терминал + файлы + reach; каталоги паков
├── journal/, memory/, secops/
└── GO_REFERENCE.md         # THIS FILE — the living spec
```

### Слойный агент (2026-08)

Полное ТЗ — `ТЗ_слойный_агент.md` в корне репозитория, рабочие правила — `AGENTS.md`.
Инвариант «LLM не выдумывает числа» усилен: разбор графика со скриншота больше НЕ берёт
цены из зрения — vision отдаёт только структуру и тикер, а числа приходят из `analyze_ticker`
(Binance). Ворота (`guardToolCall`) стоят на фактическом tool-call, не на маршрутизации.

### Рабочий стол и длина рук (2026-08-13)

Пак `system` даёт агенту ПК целиком за пределами браузера: снимок настоящего экрана или окна,
список окон, фокус, печать текста (любой алфавит — SendInput с KEYEVENTF_UNICODE), сочетания
клавиш, мышь, процессы, запуск программ, буфер обмена. Всё через WinAPI из процесса агента,
без внешних зависимостей.

Две вещи, которые тут легко сломать обратно:

- **Пиксели берутся из `CreateDIBSection`, а не из `GetDIBits`.** Измерено: `GetDIBits` на этой
  машине отдаёт строки только для областей примерно до мегабайта, а на 3440×1440 возвращает 0
  и при этом `GetLastError` = «операция выполнена успешно». Молчаливый ноль на полном экране
  при рабочем снимке 500×500 — ровно тот баг, который проходит проверку «ну работает же».
- **`launch_app` ждёт окно запущенной программы.** Без этого `type_keyboard` следующим шагом
  печатает в то окно, что было активно ДО запуска, — на живом прогоне текст уехал мимо
  Блокнота, а отчёт всё равно был «текст введён». Теперь все инструменты ввода называют окно,
  в которое ушли символы, а `findWindowAll` при неоднозначности возвращает и остальных
  кандидатов.

Режимы рук переписаны: недостижимых действий больше нет. `safe` — рискованное спрашивает,
ядерное отказывает; `full` — рискованное молча, ядерное спрашивает один раз. Промпт
исполнителя зависит от режима (`armouryNote(packs, mode)`) — иначе рубильник переключает
ворота, но не то, что модель о себе думает.

`compact.go` даёт `/compact` и автосжатие: старая часть диалога пересказывается в сводку
вместо того, чтобы молча вылететь из скользящего окна. Занятость контекста в панели — это
`prompt_n` последнего запроса (считает llama.cpp), вес истории — оценка по символам; поля
разные и не смешиваются.

### Видео: контейнер, а не модальность (2026-08-13)

Слоя «понимания видео» в движке нет и не планируется. Есть ffmpeg, который режет ролик на то,
что движок уже умеет глотать: дорожка речи уходит в тот же STT, что и голосовые, кадры — в то
же зрение, что и присланные картинки, метаданные читает ffprobe. Из этого следует всё остальное:

- **Длина ролика упирается в диск, а не в контекст.** Один проход декодирования в 16 кГц моно
  WAV, затем нарезка сегментным мультиплексором по 5 минут. Метки времени точны по построению
  (PCM с постоянным битрейтом), исходник декодируется ровно один раз.
- **В контекст модели уходит выжимка**, полная расшифровка — файлом рядом. Длинный текст
  сворачивается картой-свёрткой: куски по 6000 символов, раунды повторяются, пока не влезет.
  Измерено на живой модели: 22 800 → 584 символа за 17 с.
- **Приоритет источников:** субтитры хостинга → субтитры внутри контейнера → распознавание.
  Первые два бесплатны и точнее. Для ссылки без кадров качается только звук.
- **Артефакты лежат в `runtime/browser/media`.** Не потому, что это красиво, а потому что
  `/api/files` отдаёт панели только корень `runtime/browser`: файл мимо него остаётся без
  ссылки. Пак `system` уже наступал на это (`runtime/browser/desktop`).

Два дефекта в `chunkText` поймал тест, а не живой ролик, и оба стоили бы дорого: при остатке
места в один символ отступ до пробела обнулял длину куска — цикл крутился вечно и вешал движок
на пересказе; а при малом остатке слова дробились по буквам. Правило простое: если в остаток
не влезает целое слово — закрывай кусок, а не добирай по символу.

Проверка сделана на синтезированном ролике (SAPI наговаривает текст, слайд рисуется через
System.Drawing) — ловушка «проверил на файле, которого нет у других» так не возникает:
`video_live_test.go` запускается только с `KIBBORG_LIVE_MEDIA=путь`. На нём же видно, зачем
нужны ОБА канала: распознавание услышало «Rectrade» вместо «Freqtrade», а зрение прочитало со
слайда `github.com/freqtrade/freqtrade` дословно — и агент нашёл репозиторий.

### Подключение к Chrome: четыре грабли, каждая измерена (2026-08-13)

- **Первый `chromedp.Run` обязан идти на самом `pageCtx`.** `RemoteAllocator.Allocate` вешает
  уборку на контекст, где произошла аллокация; передав туда дочерний контекст с таймаутом и
  отменив его по `defer`, мы получали `target.CloseTarget` на вкладке, к которой только что
  подключились. Единственная вкладка — и Chrome выходил целиком. Выглядело как «браузер умирает
  от подключения агента». Таймаут теперь снаружи, в `select`, выход — через `detachPage`.
- **Адрес отладки сверяется на каждом вызове.** В браузерном ws сидит идентификатор запуска;
  после перезапуска Chrome закэшированный адрес отвечает 404, и агент терял браузер до
  перезапуска движка.
- **Порт бывает только на IPv6.** Chrome не всегда открывает 9222 на `127.0.0.1`;
  `devtoolsGet` пробует и `[::1]`, запоминая рабочий. Прежде чем чинить код — посмотри
  `Get-NetTCPConnection -LocalPort 9222`: два браузера на одном порту (IPv4 + IPv6) дают
  разные списки вкладок и необъяснимые «no target with given id».
- **Домен Network включается лениво**, а не при подключении: на живом приложении инструментовка
  сети не нужна в 19 задачах из 20.

`ClickText` ищет элемент по надписи через JS, а жмёт настоящим `Input.dispatchMouseEvent` —
`el.click()` из JS Telegram Web игнорирует, и клик «успешно» не делает ничего.

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
- `build.cmd` succeeds cleanly (gofmt + go vet + staticcheck + go build).

This is the future canonical implementation. The Python trading brain is now legacy. All new deterministic trading math goes here.

**How to use as reference during migration:**
1. build.cmd   (lint gate; do not use bare `go build` for release/start)
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
