# Graph Report - Kibborg_DaVinchi_Bot  (2026-08-27)

## Corpus Check
- 224 files · ~285,085 words
- Verdict: corpus is large enough that graph structure adds value.

## Summary
- 2675 nodes · 6916 edges · 140 communities (124 shown, 16 thin omitted)
- Extraction: 78% EXTRACTED · 22% INFERRED · 0% AMBIGUOUS · INFERRED: 1551 edges (avg confidence: 0.85)
- Token cost: 0 input · 0 output

## Graph Freshness
- Built from commit: `7285db74`
- Run `git rev-parse HEAD` and compare to check if the graph is stale.
- Run `graphify update .` after code changes (no API cost).

## Community Hubs (Navigation)
- Session
- AnalyzeSymbol
- TZ.md
- Task
- SizePosition
- Store
- agent.go
- Gerchik Trader QA — алгоритм Герчика: полный справочник для проверки
- net/http.ResponseWriter
- brain_switch.go
- tts.go
- engine-go/video.go
- runExecutorLoop
- desktop_windows.go
- handleCallbackQuery
- models_hub.go
- ScanText
- search.go
- guard.go
- futures.go
- chatWithHistoryStream
- testing.T
- Config
- document.go
- numbers.go
- llm.go
- context.Context
- rsi.go
- routeWebMessage
- browser/video.go
- New
- gerchik_test.go
- Bar
- runLayeredAgent
- handleTelegramVideo
- status_line.go
- Session
- What You Must Do When Invoked
- What You Must Do When Invoked
- Pending
- lookAtScreen
- ToolSpec
- compactChatHistory
- safeActor
- What You Must Do When Invoked
- desktop_other.go
- newTestLoop
- RunCommand
- Session
- webui_test.go
- classifyMedia
- README.md
- Handler
- newTask
- localtools.go
- 📊 Распределение модели по слоям и памяти — Kibborg DaVinchi Bot
- time.Duration
- speechText
- handleMessage
- secops_wire.go
- tool_args.go
- Python → Go Migration Plan for Kibborg_DaVinchi_Bot
- 2️⃣ Стек, который реально используют в веб-аудите (2025–2026)
- Narrative Findings (AI reviewer)
- bench-gpu.ps1
- tts_affinity_windows.go
- newWebMux
- stripThink
- WriteSecurityReport
- llmChat
- stopActiveTask
- parseProseToolCalls
- KIBBORG GO — Canonical Reference Implementation
- openTemp
- Kibborg — локальный ИИ-ассистент с双手 (агентный движок)
- Kibborg — правила для агентов
- .CaptureScreenshot
- TestMain
- graphify reference: extra exports and benchmark
- graphify
- kibborg/engine
- graphify reference: extra exports and benchmark
- graphify reference: extra exports and benchmark
- AuditFile
- Дополнительные модули
- Установка с нуля
- 💻️ Мощность железа — Kibborg DaVinchi Bot
- 17. Особенности дизайна
- Package Structure (Reference)
- Плейбук: авторизованный тест на прочность
- Kibborg Go Engine - Файлы запуска (Меню)
- TypeWhisper + Kibborg
- Скачать модель
- 6. Модальности ввода
- 7. Трейдинг
- graphify reference: query, path, explain
- graphify reference: query, path, explain
- graphify reference: query, path, explain
- Browser Agent — спецификация (Go-порт ТЗ)
- SETUP_HARDWARE.md
- Настройки `settings.ini`
- 2. Архитектура: слой за слоем
- 8. Browser Agent (`browser/`)
- graphify reference: add a URL and watch a folder
- graphify reference: commit hook and native AGENTS.md integration
- graphify reference: incremental update and cluster-only
- graphify reference: add a URL and watch a folder
- graphify reference: commit hook and native CLAUDE.md integration
- graphify reference: incremental update and cluster-only
- graphify reference: add a URL and watch a folder
- graphify reference: commit hook and native CLAUDE.md integration
- graphify reference: incremental update and cluster-only
- Что нужно перед установкой
- 12. Безопасность документов и логи
- 13. Сборка и запуск
- 15. Конфигурация
- 4. Интерфейсы
- 5. Безопасность
- graphify reference: GitHub clone and cross-repo merge
- graphify reference: transcribe video and audio
- graphify reference: GitHub clone and cross-repo merge
- graphify reference: transcribe video and audio
- graphify reference: GitHub clone and cross-repo merge
- graphify reference: transcribe video and audio
- .agents/skills/graphify/references/extraction-spec.md
- CLAUDE.md
- .claude/CLAUDE.md
- .claude/skills/graphify/references/extraction-spec.md
- .codex/skills/graphify/references/extraction-spec.md
- hacker-tools/README.md
- models_fix_test.go
- capAgentText
- installShutdownHook
- applyGPUPowerLimits
- brainServerArgs
- resetChatContext

## God Nodes (most connected - your core abstractions)
1. `Config` - 161 edges
2. `handleMessage()` - 73 edges
3. `Task` - 59 edges
4. `capAgentText()` - 49 edges
5. `routeWebMessage()` - 47 edges
6. `ToolResult` - 46 edges
7. `runLayeredAgent()` - 37 edges
8. `safeActor()` - 35 edges
9. `Session` - 33 edges
10. `argString()` - 32 edges

## Surprising Connections (you probably didn't know these)
- `stopBrainOnPort()` --calls--> `forgetBrainProc()`  [INFERRED]
  engine-go/brain_switch.go → engine-go/shutdown.go
- `main()` --calls--> `loadHistory()`  [INFERRED]
  engine-go/main.go → engine-go/history.go
- `handleWebImageTurn()` --calls--> `imageUploadMime()`  [INFERRED]
  engine-go/webui.go → engine-go/media.go
- `handleAPIModelCancel()` --calls--> `cancelModelDownload()`  [INFERRED]
  engine-go/models_web.go → engine-go/models_hub.go
- `telegramStatusText()` --calls--> `getBrowserSession()`  [INFERRED]
  engine-go/telegram_menu.go → engine-go/agent.go

## Import Cycles
- None detected.

## Communities (140 total, 16 thin omitted)

### Community 0 - "Session"
Cohesion: 0.05
Nodes (34): CapturedRequest, cloner, CloneResult, DOMSummary, FormField, FormInfo, ImageRef, Link (+26 more)

### Community 1 - "AnalyzeSymbol"
Cohesion: 0.06
Nodes (74): AnalyzeSymbol(), containsString(), countSeverity(), dataQualityStatus(), decideVerdict(), orderflowConfirm(), planInputs(), regimeHasWarning() (+66 more)

### Community 2 - "TZ.md"
Cohesion: 0.06
Nodes (33): 10. Finding Normalizer, 11. Correlation Engine, 12. Validation Engine, 13. Risk Engine, 14. Code Security Engine, 15. Browser Agent, 16. Security Memory, 17. Security Baseline (+25 more)

### Community 3 - "Task"
Cohesion: 0.09
Nodes (64): CompactResult, dispatchDocumentTool(), argString(), ResultStatus, safeEnginePath(), dispatchSystemTool(), focusIfAsked(), desktopWindow (+56 more)

### Community 4 - "SizePosition"
Cohesion: 0.07
Nodes (46): cmdArgs, gerchikSizingBlock(), BuildPlan(), invalidation(), roundPrice(), roundPriceSlice(), TestBuildPlanLong(), TestBuildPlanNonDirectional() (+38 more)

### Community 5 - "Store"
Cohesion: 0.08
Nodes (22): brainSwitchState, decodeFloats(), encodeFloats(), Open(), round2(), scanTrade(), cosine(), decodeVec() (+14 more)

### Community 6 - "agent.go"
Cohesion: 0.05
Nodes (67): agentURLBagNote(), capLog(), clearAgentURLs(), extractHTTPURLs(), getAgentURLs(), isContextBad(), isContextOverflow(), isVideoExt() (+59 more)

### Community 7 - "Gerchik Trader QA — алгоритм Герчика: полный справочник для проверки"
Cohesion: 0.04
Nodes (46): 10. Крипто-специфика, 11. Что нельзя формализовать (субъективное), 12. Расхождения между источниками курса, 13. Процедура аудита, 14. Типичные ошибки реализации (красные флаги), 15. Формулы для сверки с кодом, 16. Что должно быть покрыто тестами, 17. Форма отчёта аудита (+38 more)

### Community 8 - "net/http.ResponseWriter"
Cohesion: 0.15
Nodes (34): localModelCards(), startBrainSwitch(), switchSnapshot(), contextSnapshot(), downloadSnapshot(), liveBrainModelNow(), handleAPIHardware(), handleAPIModelAssign() (+26 more)

### Community 9 - "brain_switch.go"
Cohesion: 0.28
Nodes (12): brainSwitchBusy(), killPID(), parsePIDList(), pidsListening(), pidsListeningUnix(), pidsListeningWindows(), runBrainSwitch(), setBrainSwitch() (+4 more)

### Community 10 - "tts.go"
Cohesion: 0.09
Nodes (39): SpeechFile, elapsedMS(), ensureTTS(), isTTSTimeout(), prettyHost(), pulseTTSAffinity(), rememberSpeakable(), respreadTTSAfterFirstSynth() (+31 more)

### Community 11 - "engine-go/video.go"
Cohesion: 0.08
Nodes (55): convertOpts, frameShot, mediaAnalyzeOpts, mediaDigest, mediaInfo, mediaSource, mediaStream, transcriptChunk (+47 more)

### Community 12 - "runExecutorLoop"
Cohesion: 0.13
Nodes (25): compactToolMessages(), estimateAgentChars(), agentBusyNotice(), checkInterrupted(), claimsNoTools(), finishTask(), loopState, readNudge() (+17 more)

### Community 13 - "desktop_windows.go"
Cohesion: 0.10
Nodes (36): bitmapInfo, bitmapInfoHeader, blockedInputHint(), capturePNG(), captureScreenPNG(), captureWindowPNG(), clickMouse(), findWindow() (+28 more)

### Community 14 - "handleCallbackQuery"
Cohesion: 0.09
Nodes (43): handleCompactCommand(), TestHandsEndpointReadsAndWrites(), currentHandsMode(), handsModeLabel(), handsModeShort(), loadHandsMode(), normalizeHandsMode(), setHandsMode() (+35 more)

### Community 15 - "models_hub.go"
Cohesion: 0.05
Nodes (73): activity, assignedPaths, findLocalWeight(), catalogEntry, CPUInfo, DiskInfo, FitResult, GenStats (+65 more)

### Community 16 - "ScanText"
Cohesion: 0.11
Nodes (31): capSample(), classify(), looksLikeFile(), redactThreatSample(), ScanText(), sevIcon(), sevRank(), findIOC() (+23 more)

### Community 17 - "search.go"
Cohesion: 0.15
Nodes (31): enginePack, searchEngine, SearchResult, blockedReason(), chromeSearch(), cleanSearchURL(), decodeDDG(), engineBing() (+23 more)

### Community 18 - "guard.go"
Cohesion: 0.11
Nodes (40): Action, Decision, allowD(), askD(), autostartDirs(), blockD(), classifyToolCall(), commandPaths() (+32 more)

### Community 19 - "futures.go"
Cohesion: 0.15
Nodes (31): FlowBar, fundingEvent, FundingPrint, attachFlowToTimeframes(), buildCVDCandles(), buildLevelCandles(), chartFundingInterval(), fapiHostOrder() (+23 more)

### Community 20 - "chatWithHistoryStream"
Cohesion: 0.12
Nodes (25): htmlEscape(), renderEmphasis(), renderInline(), stripTags(), toTelegramHTML(), liveMessage, sendTelegramChunk(), sendTelegramRaw() (+17 more)

### Community 21 - "testing.T"
Cohesion: 0.08
Nodes (39): TestFindLocalWeightExactAndAmbiguous(), TestInferCapsQwen38(), TestParsePIDList(), tableFromHTML(), TestActAndReadToolMapsMatchPacks(), TestAllocatorFollowsChromeRestart(), TestCapStrKeepsValidUTF8(), TestClickElementAcceptsTextAndSelector() (+31 more)

### Community 22 - "Config"
Cohesion: 0.17
Nodes (31): Config, atofDefault(), atoiDefault(), boolDefault(), loadConfig(), portInUse(), handleScanCommand(), secNarration() (+23 more)

### Community 23 - "document.go"
Cohesion: 0.12
Nodes (37): docDigest, docHit, docPage, docReadOpts, collapseRuns(), docCachePath(), docLanguage(), documentReady() (+29 more)

### Community 24 - "numbers.go"
Cohesion: 0.16
Nodes (28): allDigits(), alreadySpelled(), annotateLine(), collapseDupParens(), collapseDupParensOnce(), collectSpans(), digitsOnly(), fracToCents() (+20 more)

### Community 25 - "llm.go"
Cohesion: 0.23
Nodes (15): assistantToolMsg(), assistantMsg, applyToolSampling(), brainLogPath(), ensureBrain(), liveBrainPID(), llamaProcEnv(), llmChatTools() (+7 more)

### Community 26 - "context.Context"
Cohesion: 0.16
Nodes (32): toJSON(), AgentReachDoctor(), AgentReachRun(), cleanSubtitle(), excerptBody(), formatSearchWithHarvest(), GitHubSearch(), harvestPages() (+24 more)

### Community 27 - "rsi.go"
Cohesion: 0.07
Nodes (62): timeframeData(), ATR(), EMA(), EMALast(), MACDHist(), RSI(), rsiFromAvg(), RSISeries() (+54 more)

### Community 28 - "routeWebMessage"
Cohesion: 0.17
Nodes (31): runAgent(), maybeAutoCompact(), bytesHaveNUL(), chatWithFile(), actorFor(), brainReady(), baseMessages(), recordHistory() (+23 more)

### Community 29 - "browser/video.go"
Cohesion: 0.16
Nodes (23): VideoDownload, TestIsVideoURL(), ExtractVideoURL(), FetchMedia(), FetchVideo(), fileExists(), FormatVideoResult(), Session (+15 more)

### Community 30 - "New"
Cohesion: 0.24
Nodes (17): TestUnparsedToolCallDetected(), New(), packEscalationResult, assemblePackTools(), normalizePacks(), packToolNames(), requestPack(), schemaChars() (+9 more)

### Community 31 - "gerchik_test.go"
Cohesion: 0.16
Nodes (32): buildDirectional(), detectModel(), contains(), day(), dirOf(), hasBlock(), hasLevelNear(), indexOf() (+24 more)

### Community 32 - "Bar"
Cohesion: 0.06
Nodes (77): candle, flowQuadName(), renderFlow(), absF(), renderGerchik(), renderSetup(), round4(), signHalf() (+69 more)

### Community 33 - "runLayeredAgent"
Cohesion: 0.28
Nodes (21): assistantText(), assistantToolCall(), checkToolProtocol(), containsAll(), newFakeBrain(), splitHostPort(), TestE2EDesktopScreenshotReachesUser(), TestE2EDispatcherGarbageFallsBackToWeb() (+13 more)

### Community 34 - "handleTelegramVideo"
Cohesion: 0.25
Nodes (14): handleTelegramPDF(), handleWebPDFTurn(), downloadTelegramFileTo(), humanBytes(), downloadTelegramVideo(), handleTelegramVideo(), ingestDigestPrompt(), saveUploadedVideo() (+6 more)

### Community 35 - "status_line.go"
Cohesion: 0.16
Nodes (24): absInt(), clipStatus(), firstArg(), formatInt(), loopState, itoa(), oneLine(), pageTitleHint() (+16 more)

### Community 36 - "Session"
Cohesion: 0.12
Nodes (4): assertPublicTabURL(), Session, enableCaptureDomains(), github.com/chromedp/chromedp.Action

### Community 37 - "What You Must Do When Invoked"
Cohesion: 0.08
Nodes (24): For /graphify add and --watch, For /graphify query, For the commit hook and native AGENTS.md integration, For --update and --cluster-only, /graphify, Honesty Rules, Interpreter guard for subcommands, Part A - Structural extraction for code files (+16 more)

### Community 38 - "What You Must Do When Invoked"
Cohesion: 0.08
Nodes (24): For /graphify add and --watch, For /graphify query, For the commit hook and native CLAUDE.md integration, For --update and --cluster-only, /graphify, Honesty Rules, Interpreter guard for subcommands, Part A - Structural extraction for code files (+16 more)

### Community 39 - "Pending"
Cohesion: 0.16
Nodes (24): Pending, clearPending(), clearPersistedPending(), expirePendingLoop(), newPendingID(), peekPending(), persistPending(), renderPendingArgs() (+16 more)

### Community 40 - "lookAtScreen"
Cohesion: 0.27
Nodes (11): brainHasVision(), lookAtScreen(), parseFindings(), renderFindings(), shrinkPNG(), TestParseFindingsMapsToScreenCoordinates(), TestParseFindingsRejectsGarbage(), TestRenderFindingsWarnsAboutPrecision() (+3 more)

### Community 41 - "ToolSpec"
Cohesion: 0.18
Nodes (26): ToolSpec, argBool(), argStrSlice(), arrp(), boolp(), DedupTools(), Session, intp() (+18 more)

### Community 42 - "compactChatHistory"
Cohesion: 0.20
Nodes (15): chatMsg, brainCtxSize(), compactChatHistory(), estimateMsgTokens(), estimateTokens(), fetchCtxSize(), historyOf(), seedHistory() (+7 more)

### Community 43 - "safeActor"
Cohesion: 0.13
Nodes (34): Actor, agentSystemPrompt(), executorSystemPrompt(), TestNonOwnerGetsDenyNotAsk(), agentRequest, TestExecutorPromptIsHandsAware(), TestExecutorPromptIsPackAware(), TestPlanAndSummaryHaveConsumers() (+26 more)

### Community 44 - "What You Must Do When Invoked"
Cohesion: 0.08
Nodes (24): For /graphify add and --watch, For /graphify query, For the commit hook and native CLAUDE.md integration, For --update and --cluster-only, /graphify, Honesty Rules, Interpreter guard for subcommands, Part A - Structural extraction for code files (+16 more)

### Community 45 - "desktop_other.go"
Cohesion: 0.15
Nodes (11): captureScreenPNG(), captureWindowPNG(), findWindow(), findWindowAll(), focusDesktopWindow(), foregroundWindow(), desktopWindow, screenRect (+3 more)

### Community 46 - "newTestLoop"
Cohesion: 0.28
Nodes (17): hasPack(), answeredIDs(), call(), newTestLoop(), TestBadArgumentsAreFailure(), TestDeadToolDoesNotBecomeTheAnswer(), TestDeniedCallReportsDeniedStatus(), TestFailedActionsSurfaceToUser() (+9 more)

### Community 47 - "RunCommand"
Cohesion: 0.16
Nodes (17): DeleteLocal(), isMostlyBinary(), ListLocalDir(), LocalFileInfo(), MkdirLocal(), ReadLocalFile(), WriteLocalFile(), capCmdBytes() (+9 more)

### Community 48 - "Session"
Cohesion: 0.07
Nodes (34): Tab, getBrowserSession(), getBrowserSessionWithFFmpeg(), TestSameLocationIgnoresFragmentOnly(), altLoopback(), Session, isStalePageErr(), sameLocation() (+26 more)

### Community 49 - "webui_test.go"
Cohesion: 0.19
Nodes (15): inputAcceptFilter(), TestAnalyzePageHasRSIOscillator(), TestLiveTokensReachTheUI(), TestModelsTabAndHardwareAPI(), TestSecurityTabHasConfirmAndContext(), TestUploadAcceptCoversSupportedKinds(), TestWebCandlesValidation(), TestWebHeaderHasGlobalStop() (+7 more)

### Community 50 - "classifyMedia"
Cohesion: 0.12
Nodes (21): isTextFile(), TestBytesHaveNUL(), TestIsTextFile(), chatWithImage(), describeImage(), describeImageBytes(), downloadTelegramFile(), getTelegramFilePath() (+13 more)

### Community 51 - "README.md"
Cohesion: 0.10
Nodes (18): Kibborg Go Engine, Безопасность и «длина рук», Веб-панель, Документы рядом, Если что-то не работает, Железо и скорость, За 30 секунд, Как пользоваться (+10 more)

### Community 52 - "Handler"
Cohesion: 0.23
Nodes (7): Any, BaseHTTPRequestHandler, Handler, load_model(), _resolve_lang(), _resolve_voice(), synthesize()

### Community 53 - "newTask"
Cohesion: 0.23
Nodes (11): activeTask(), newTask(), newTaskID(), TestArtifactsNeverEnterModelContext(), TestConfirmWordParsing(), TestInterruptedNotRemembered(), TestInterruptedStatusesDiffer(), TestIsOwnerChat() (+3 more)

### Community 54 - "localtools.go"
Cohesion: 0.11
Nodes (29): findHackerToolsFile(), LoadCatalog(), PlaybookPath(), RenderCatalogMarkdown(), ResetCatalogCacheForTest(), SearchCatalog(), chdirEngineGo(), TestLoadAndSearchCatalog() (+21 more)

### Community 55 - "📊 Распределение модели по слоям и памяти — Kibborg DaVinchi Bot"
Cohesion: 0.10
Nodes (19): KIBORG-LONG профиль, n-gpu-layers 99, Tensor split: 0.35, 0.65, 🔌 Блок питания, 🎮 Видеокарты, ⚡ Итог одним взглядом, Квоты контекста, 📦 Классификация файлов GGUF (по hardware.go) (+11 more)

### Community 56 - "time.Duration"
Cohesion: 0.16
Nodes (16): blockInternalRedirects(), dialPublicOnly(), hostIsInternal(), ipIsInternal(), looksNonCanonicalIP(), redirectSafeClient(), safeArtifactPath(), safeHTTPClient() (+8 more)

### Community 57 - "speechText"
Cohesion: 0.15
Nodes (20): annotateNumbers(), collapseAdjacentParens(), collapseAdjacentParensOnce(), TestAnnotateDoesNotDoubleExistingWords(), TestAnnotateNumbersAddsWords(), TestAnnotateSkipsAlreadySpelled(), TestAnnotateSkipsURLAndYear(), TestCollapseAdjacentNumberParens() (+12 more)

### Community 58 - "handleMessage"
Cohesion: 0.12
Nodes (46): wantsToolAgent(), TestWebVoiceAndTextShareOneRoute(), isOwnerChat(), answerTelegramConfirmation(), chartTaskText(), chatWorker(), deliverAgentResult(), enqueueMessage() (+38 more)

### Community 59 - "secops_wire.go"
Cohesion: 0.25
Nodes (16): capBytes(), handleLogsCommand(), handleSecFile(), looksTextish(), normalizeStressMode(), parseStressModeToken(), readOwnLogs(), splitStressArg() (+8 more)

### Community 60 - "tool_args.go"
Cohesion: 0.16
Nodes (16): plainErr, ensureValidToolArgsJSON(), fixToolCallMapArgs(), isBadToolCallArgs(), parseToolArgs(), repairToolArgsJSON(), sanitizeMsgsToolArgs(), sanitizeToolCalls() (+8 more)

### Community 61 - "Python → Go Migration Plan for Kibborg_DaVinchi_Bot"
Cohesion: 0.12
Nodes (16): Architecture During Transition (recommended), Deliverables of a Successful Migration, Estimated Difficulty, Phase 0 — Preparation (do this first, language-agnostic), Phase 1 — Trading Brain + Core Deterministic Engine (highest ROI, lowest risk), Phase 2 — Computer Tools Server (the "organs" + tools surface), Phase 3 — Bot Orchestration Layer (the hard part), Phase 4 — AdilBot copilot loops + ancillary services (+8 more)

### Community 62 - "2️⃣ Стек, который реально используют в веб-аудите (2025–2026)"
Cohesion: 0.14
Nodes (13): 1️⃣ Что из Awesome-Hacking стоит агенту, а что мусор, 2️⃣ Стек, который реально используют в веб-аудите (2025–2026), 3️⃣ Что агенту индексировать из GitHub (короткий allowlist), 4️⃣ Как это класть в KIBBORG-агента (архитектура, не плейбук атаки), 5️⃣ Честная оценка «что будет работать», 🤖 AI-агенты под pentest (как раз под твою задачу «агент в помощника»), 🌐 API / GraphQL, 🧪 DAST / сканеры веб-приложений (+5 more)

### Community 63 - "Narrative Findings (AI reviewer)"
Cohesion: 0.13
Nodes (14): CR-01: `run_command taskkill/Stop-Process` bypasses critical-process gate, CR-02: Chrome `Navigate` follows redirects to loopback — open_url / pageText SSRF, IN-01 / Medium: `/api/status` (and `/icons/`) outside `sameOriginGuard`, IN-02 / Medium: Hands mode still snapshotted for in-flight tool calls, IN-03 / Medium: Telegram confirm without pending id — acceptable; Web chat «да» weaker than buttons, Narrative Findings (AI reviewer), Phase secops-meta-security: Residual Security Sweep, Summary (+6 more)

### Community 64 - "bench-gpu.ps1"
Cohesion: 0.22
Nodes (3): Build-Prompt(), Invoke-Json(), Tokenize()

### Community 65 - "tts_affinity_windows.go"
Cohesion: 0.27
Nodes (9): groupAffinity, processEntry32W, threadEntry32, affThread(), childPIDs(), setProcessAllCpuSets(), spreadPIDAcrossCPUGroups(), spreadPIDTree() (+1 more)

### Community 66 - "newWebMux"
Cohesion: 0.18
Nodes (16): TestAgentControlEndpointsAreCSRFGuarded(), TestSameOrigin_BlocksCrossSiteAndRebinding(), TestSameOriginGuard_Returns403(), TestStopEndpointWithoutTask(), TestAssignBrainModelClearsMMProj(), registerModelRoutes(), registerSecurityRoutes(), registerTTSRoutes() (+8 more)

### Community 67 - "stripThink"
Cohesion: 0.24
Nodes (13): summariseDialog(), embedEnabled(), embedReady(), embedText(), ensureEmbed(), stripThink(), capMem(), initMemory() (+5 more)

### Community 68 - "WriteSecurityReport"
Cohesion: 0.33
Nodes (8): buildSecurityMarkdown(), ListSecurityReports(), sanitizeReportName(), TestWriteSecurityReport(), TestWriteSecurityReport_RequiresFields(), WriteSecurityReport(), SecurityReportInput, SecurityReportResult

### Community 69 - "llmChat"
Cohesion: 0.36
Nodes (7): applyChatSampling(), applyGenLimit(), TestCoerceChatMessages_MergesSystems(), TestCoerceChatMessages_NoSystem(), coerceChatMessages(), llmChat(), llmChatStream()

### Community 70 - "stopActiveTask"
Cohesion: 0.38
Nodes (4): registerTask(), stopActiveTask(), TestStopOnlyHitsLiveTask(), unregisterTask()

### Community 71 - "parseProseToolCalls"
Cohesion: 0.15
Nodes (24): armouryNote(), hasControlChars(), identBefore(), indexTopLevel(), isIdentByte(), isIdentifier(), isPlaceholder(), matchParen() (+16 more)

### Community 72 - "KIBBORG GO — Canonical Reference Implementation"
Cohesion: 0.18
Nodes (10): Building a Reference-Grade Port, Core Philosophy & Non-Negotiable Invariant, Current Implementation Status (Reference Build) - 2026-06, Future Roadmap (inside this reference), High-Level Architecture (Go as the Future), How to Use During Migration (Practical), Invariants & Rules for Contributors (Go side), Key Data Model — DecisionReport (the contract) (+2 more)

### Community 73 - "openTemp"
Cohesion: 0.50
Nodes (7): approx(), Store, openTemp(), TestCancelAndList(), TestCloseTradeComputesPnLAndR(), TestCloseTradeShortLossAndBreakeven(), TestStatsAggregate()

### Community 74 - "Kibborg — локальный ИИ-ассистент с双手 (агентный движок)"
Cohesion: 0.20
Nodes (9): 10. Озвучка (TTS), 11. Сжатие контекста (`/compact`), 14. Структура файлов, 16. Зависимости, 18. Быстрые команды, 1. Что это такое, 3. Паки инструментов, 9. Долговременная память (+1 more)

### Community 75 - "Kibborg — правила для агентов"
Cohesion: 0.20
Nodes (9): graphify (экономия токенов), Kibborg — правила для агентов, Безопасность, Запуск и останов (весь стек), Как соблюдать при новой фиче, Паки инструментов, Паритет Telegram ↔ Web (обязательно), Сборка и линтер (обязательно) (+1 more)

### Community 80 - "graphify reference: extra exports and benchmark"
Cohesion: 0.22
Nodes (8): graphify reference: extra exports and benchmark, Step 6b - Wiki (only if --wiki flag), Step 7 - Neo4j export (only if --neo4j or --neo4j-push flag), Step 7a - FalkorDB export (only if --falkordb or --falkordb-push flag), Step 7b - SVG export (only if --svg flag), Step 7c - GraphML export (only if --graphml flag), Step 7d - MCP server (only if --mcp flag), Step 8 - Token reduction benchmark (only if total_words > 5000)

### Community 86 - "graphify reference: extra exports and benchmark"
Cohesion: 0.22
Nodes (8): graphify reference: extra exports and benchmark, Step 6b - Wiki (only if --wiki flag), Step 7 - Neo4j export (only if --neo4j or --neo4j-push flag), Step 7a - FalkorDB export (only if --falkordb or --falkordb-push flag), Step 7b - SVG export (only if --svg flag), Step 7c - GraphML export (only if --graphml flag), Step 7d - MCP server (only if --mcp flag), Step 8 - Token reduction benchmark (only if total_words > 5000)

### Community 87 - "graphify reference: extra exports and benchmark"
Cohesion: 0.22
Nodes (8): graphify reference: extra exports and benchmark, Step 6b - Wiki (only if --wiki flag), Step 7 - Neo4j export (only if --neo4j or --neo4j-push flag), Step 7a - FalkorDB export (only if --falkordb or --falkordb-push flag), Step 7b - SVG export (only if --svg flag), Step 7c - GraphML export (only if --graphml flag), Step 7d - MCP server (only if --mcp flag), Step 8 - Token reduction benchmark (only if total_words > 5000)

### Community 88 - "AuditFile"
Cohesion: 0.27
Nodes (9): AuditFile(), entropyNote(), isMostlyText(), shannonEntropy(), sniffType(), TestAuditFile_Entropy(), TestAuditFile_KnownHashes(), TestAuditFile_TypeSniff() (+1 more)

### Community 89 - "Дополнительные модули"
Cohesion: 0.22
Nodes (9): 1. FFmpeg — голос и видео, 2. yt-dlp — скачивание видео, 3. Google Chrome — живой браузер, 4. TypeWhisper — голос (рекомендуется), 5. Poppler + Tesseract — PDF и сканы, 5. Qwen3-TTS — озвучка ответов, 6. Python + Agent Reach (по желанию), 7. Telegram-бот (+1 more)

### Community 90 - "Установка с нуля"
Cohesion: 0.22
Nodes (9): Установка с нуля, Шаг 1. Скачать код, Шаг 2. Поставить Go, Шаг 3. Поставить `llama-server` с CUDA, Шаг 4. Скачать модель, Шаг 5. Создать `settings.ini`, Шаг 6. Поставить дополнительные модули, Шаг 7. Собрать движок (+1 more)

### Community 91 - "💻️ Мощность железа — Kibborg DaVinchi Bot"
Cohesion: 0.22
Nodes (8): 🔌 Блок питания, 🎮 Видеокарты, ⚡ Итог одним взглядом, 🔲 Материнская плата, 💻️ Мощность железа — Kibborg DaVinchi Bot, 💾 Накопители, 🎞 Оперативная память, 🧠 Процессор

### Community 92 - "17. Особенности дизайна"
Cohesion: 0.29
Nodes (7): 17. Особенности дизайна, PDF — контейнер, а не модальность, Видео — контейнер, а не модальность, Нет опоры — переспроси, Паритет интерфейсов, Провалившиеся действия приписываются, Скриншот графика — цифры от биржи, не от зрения

### Community 93 - "Package Structure (Reference)"
Cohesion: 0.29
Nodes (7): Package Structure (Reference), PDF и сканы: три источника текста, порядок важнее инструментов (2026-08-13), Видео: контейнер, а не модальность (2026-08-13), Подключение к Chrome: четыре грабли, каждая измерена (2026-08-13), Рабочий стол и длина рук (2026-08-13), Разбор по Герчику (2026-08-13), Слойный агент (2026-08)

### Community 94 - "Плейбук: авторизованный тест на прочность"
Cohesion: 0.25
Nodes (7): Запрещено, Плейбук: авторизованный тест на прочность, Порядок работы, Режимы глубины, Формат находки в отчёте, Цель, Что проверять (минимум)

### Community 95 - "Kibborg Go Engine - Файлы запуска (Меню)"
Cohesion: 0.29
Nodes (6): Files, Hardware Notes (for 2x RTX 3060 12GB), Integration, Kibborg Go Engine - Файлы запуска (Меню), Quick Start (after models downloaded), Первый прогон агента

### Community 96 - "TypeWhisper + Kibborg"
Cohesion: 0.29
Nodes (6): Kibborg (`settings.ini`), TypeWhisper + Kibborg, Вайбкодинг (диктовка в терминал), Паритет Telegram ↔ Web, Установка (уже сделана на этой машине), Что уже настроено автоматически

### Community 97 - "Скачать модель"
Cohesion: 0.29
Nodes (7): Опционально: запасной Whisper, Опционально: эмбеддинги для умной памяти, Рекомендуемая (по умолчанию), Скачать модель, Способ А. Пункт меню (проще всего), Способ Б. Команда вручную, Способ В. Браузер

### Community 98 - "6. Модальности ввода"
Cohesion: 0.33
Nodes (6): 6. Модальности ввода, PDF, Видео, Голос (STT), Изображения (Vision), Ссылки на видео

### Community 99 - "7. Трейдинг"
Cohesion: 0.33
Nodes (6): 7. Трейдинг, `/chart` — торговый разбор графика, Алгоритм Герчика (`trading/gerchik.go`), Анализ тикера (`/analyze`), Журнал сделок (`/log`, `/journal`, `/close`), Позиционирование (`/size`)

### Community 100 - "graphify reference: query, path, explain"
Cohesion: 0.33
Nodes (5): For /graphify explain, For /graphify path, graphify reference: query, path, explain, Step 0 — Constrained query expansion (REQUIRED before traversal), Step 1 — Traversal

### Community 101 - "graphify reference: query, path, explain"
Cohesion: 0.33
Nodes (5): For /graphify explain, For /graphify path, graphify reference: query, path, explain, Step 0 — Constrained query expansion (REQUIRED before traversal), Step 1 — Traversal

### Community 102 - "graphify reference: query, path, explain"
Cohesion: 0.33
Nodes (5): For /graphify explain, For /graphify path, graphify reference: query, path, explain, Step 0 — Constrained query expansion (REQUIRED before traversal), Step 1 — Traversal

### Community 103 - "Browser Agent — спецификация (Go-порт ТЗ)"
Cohesion: 0.33
Nodes (5): Browser Agent — спецификация (Go-порт ТЗ), Tool Calling, Результат, Структура пакета (Go-аналог дерева из ТЗ), Что меняется при переносе Python → Go

### Community 105 - "SETUP_HARDWARE.md"
Cohesion: 0.33
Nodes (5): 1. Рекомендуемая модель (по умолчанию), 2. Прямые ссылки, 3. Команда для скачивания, 4. Полная команда запуска llama-server (оптимизировано под dual 3060), 5. Полезные советы

### Community 106 - "Настройки `settings.ini`"
Cohesion: 0.33
Nodes (6): Голос и видео, Каналы, Мозг, Настройки `settings.ini`, Память и трейдер, Руки

### Community 107 - "2. Архитектура: слой за слоем"
Cohesion: 0.40
Nodes (5): 2. Архитектура: слой за слоем, Слой 1 — Диспетчер (`dispatcher.go`), Слой 2 — Сбор рук (`packs.go`), Слой 3 — Ворота / Guard (`guard.go`), Слой 4 — Исполнение (`agent_loop.go`)

### Community 109 - "8. Browser Agent (`browser/`)"
Cohesion: 0.50
Nodes (4): 8. Browser Agent (`browser/`), Возможности, Клики в браузере — по надписи, Подключение

### Community 110 - "graphify reference: add a URL and watch a folder"
Cohesion: 0.50
Nodes (3): For /graphify add, For --watch, graphify reference: add a URL and watch a folder

### Community 111 - "graphify reference: commit hook and native AGENTS.md integration"
Cohesion: 0.50
Nodes (3): For git commit hook, For native AGENTS.md integration, graphify reference: commit hook and native AGENTS.md integration

### Community 112 - "graphify reference: incremental update and cluster-only"
Cohesion: 0.50
Nodes (3): For --cluster-only, For --update (incremental re-extraction), graphify reference: incremental update and cluster-only

### Community 113 - "graphify reference: add a URL and watch a folder"
Cohesion: 0.50
Nodes (3): For /graphify add, For --watch, graphify reference: add a URL and watch a folder

### Community 114 - "graphify reference: commit hook and native CLAUDE.md integration"
Cohesion: 0.50
Nodes (3): For git commit hook, For native CLAUDE.md integration, graphify reference: commit hook and native CLAUDE.md integration

### Community 115 - "graphify reference: incremental update and cluster-only"
Cohesion: 0.50
Nodes (3): For --cluster-only, For --update (incremental re-extraction), graphify reference: incremental update and cluster-only

### Community 116 - "graphify reference: add a URL and watch a folder"
Cohesion: 0.50
Nodes (3): For /graphify add, For --watch, graphify reference: add a URL and watch a folder

### Community 117 - "graphify reference: commit hook and native CLAUDE.md integration"
Cohesion: 0.50
Nodes (3): For git commit hook, For native CLAUDE.md integration, graphify reference: commit hook and native CLAUDE.md integration

### Community 118 - "graphify reference: incremental update and cluster-only"
Cohesion: 0.50
Nodes (3): For --cluster-only, For --update (incremental re-extraction), graphify reference: incremental update and cluster-only

### Community 119 - "Что нужно перед установкой"
Cohesion: 0.50
Nodes (4): Железо, Программы, без которых стек не встанет, Программы, без которых часть функций молчит, Что нужно перед установкой

### Community 120 - "12. Безопасность документов и логи"
Cohesion: 0.67
Nodes (3): 12. Безопасность документов и логи, Referent (`referent.go`), SecOps (`secops/`)

### Community 121 - "13. Сборка и запуск"
Cohesion: 0.67
Nodes (3): 13. Сборка и запуск, Запуск, Сборка

### Community 122 - "15. Конфигурация"
Cohesion: 0.67
Nodes (3): 15. Конфигурация, Runtime-store (не конфигурация), Ключевые параметры

### Community 123 - "4. Интерфейсы"
Cohesion: 0.67
Nodes (3): 4. Интерфейсы, Telegram-бот, Веб-панель

### Community 124 - "5. Безопасность"
Cohesion: 0.67
Nodes (3): 5. Безопасность, Полный терминал/файлы, Режим рук (hands mode)

### Community 138 - "models_fix_test.go"
Cohesion: 0.40
Nodes (5): containsInt(), TestAssignBrainModelRejectsNonGGUF(), TestBrainKillSetScopedToBrainAndPort(), TestLiveBrainModelAtomic(), setLiveBrainModel()

### Community 140 - "capAgentText"
Cohesion: 0.31
Nodes (9): capAgentText(), chunkText(), isSpaceRune(), mapReduceSummary(), liveMediaClip(), TestLiveTranscriptSummary(), TestLiveVideoAnalysis(), summariseTranscript() (+1 more)

### Community 141 - "installShutdownHook"
Cohesion: 0.22
Nodes (10): closeBrowserSession(), brainKillSet(), closeMemory(), forgetBrainProc(), installShutdownHook(), registerEngineProc(), stopLaunchedEngines(), trackBrainProc() (+2 more)

### Community 142 - "applyGPUPowerLimits"
Cohesion: 0.33
Nodes (8): applyGPUPowerLimits(), parseGPUPowerLimits(), parseWatts(), TestLoadConfigGPUPowerLimit(), TestParseGPUPowerLimits(), TestParseGPUPowerLimitsRejectsGarbage(), TestParseWattsReadsNvidiaSMIFormat(), gpuPowerRule

### Community 143 - "brainServerArgs"
Cohesion: 0.32
Nodes (7): brainServerArgs(), llamaThreadCount(), TestBrainServerArgs256KNoKVOffload(), TestBrainServerArgsKeepsKVOnGPUByDefault(), TestLlamaThreadCountAutoUsesBothSockets(), TestLoadConfig256KNoKVOffload(), TestLoadConfigNoKVOffloadDefaultOff()

### Community 151 - "resetChatContext"
Cohesion: 0.33
Nodes (6): loadHistory(), markHistoryDirty(), saveHistoryNow(), startHistorySaver(), forgetMemory(), resetChatContext()

## Knowledge Gaps
- **406 isolated node(s):** `C:/Users/lex66/AppData/Roaming/uv/tools/graphifyy/Scripts/python.exe`, `Session`, `Session`, `keybdInput`, `mouseInput` (+401 more)
  These have ≤1 connection - possible missing edges or undocumented components.
- **16 thin communities (<3 nodes) omitted from report** — run `graphify query` to explore isolated nodes.

## Suggested Questions
_Questions this graph is uniquely positioned to answer:_

- **Why does `Config` connect `Config` to `Task`, `SizePosition`, `agent.go`, `net/http.ResponseWriter`, `brain_switch.go`, `tts.go`, `engine-go/video.go`, `runExecutorLoop`, `installShutdownHook`, `handleCallbackQuery`, `applyGPUPowerLimits`, `brainServerArgs`, `capAgentText`, `chatWithHistoryStream`, `document.go`, `llm.go`, `routeWebMessage`, `Bar`, `handleTelegramVideo`, `Pending`, `lookAtScreen`, `compactChatHistory`, `safeActor`, `classifyMedia`, `handleMessage`, `secops_wire.go`, `newWebMux`, `stripThink`?**
  _High betweenness centrality (0.084) - this node is a cross-community bridge._
- **Why does `Task` connect `Task` to `Bar`, `Store`, `agent.go`, `Pending`, `stopActiveTask`, `safeActor`, `runExecutorLoop`, `guard.go`, `newTask`, `time.Duration`, `context.Context`, `tool_args.go`, `New`?**
  _High betweenness centrality (0.037) - this node is a cross-community bridge._
- **Why does `Session` connect `Session` to `Session`, `Store`, `ToolSpec`, `runExecutorLoop`, `context.Context`, `New`?**
  _High betweenness centrality (0.029) - this node is a cross-community bridge._
- **Are the 48 inferred relationships involving `handleMessage()` (e.g. with `runLayeredAgent()` and `wantsToolAgent()`) actually correct?**
  _`handleMessage()` has 48 INFERRED edges - model-reasoned connections that need verification._
- **What connects `C:/Users/lex66/AppData/Roaming/uv/tools/graphifyy/Scripts/python.exe`, `Session`, `Session` to the rest of the system?**
  _406 weakly-connected nodes found - possible documentation gaps or missing edges._
- **Should `Session` be split into smaller, more focused modules?**
  _Cohesion score 0.052083333333333336 - nodes in this community are weakly interconnected._
- **Should `AnalyzeSymbol` be split into smaller, more focused modules?**
  _Cohesion score 0.05910364145658263 - nodes in this community are weakly interconnected._