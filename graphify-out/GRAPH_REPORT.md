# Graph Report - Kibborg_DaVinchi_Bot  (2026-08-28)

## Corpus Check
- 246 files · ~314,209 words
- Verdict: corpus is large enough that graph structure adds value.

## Summary
- 2846 nodes · 7273 edges · 157 communities (141 shown, 16 thin omitted)
- Extraction: 78% EXTRACTED · 22% INFERRED · 0% AMBIGUOUS · INFERRED: 1622 edges (avg confidence: 0.85)
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
- 1.md
- Gerchik Trader QA — алгоритм Герчика: полный справочник для проверки
- capAgentText
- инстументы для кибербезопасности.md
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
- Session
- context.Context
- rsi.go
- indicators.go
- browser/video.go
- New
- gerchik_test.go
- history.go
- market.go
- status_line.go
- DownloadURL
- Session
- What You Must Do When Invoked
- What You Must Do When Invoked
- runLayeredAgent
- lookAtScreen
- ToolSpec
- compactChatHistory
- safeActor
- What You Must Do When Invoked
- desktop_other.go
- newTestLoop
- .Dispatch
- probe.go
- browser/schema.go
- .fetchAs
- README.md
- Handler
- newTask
- secops_wire.go
- 📊 Распределение модели по слоям и памяти — Kibborg DaVinchi Bot
- safeRemoteURL
- .executeGuarded
- handleMessage
- webui_test.go
- tool_args.go
- Python → Go Migration Plan for Kibborg_DaVinchi_Bot
- 2️⃣ Стек, который реально используют в веб-аудите (2025–2026)
- Narrative Findings (AI reviewer)
- bench-gpu.ps1
- tts_affinity_windows.go
- Table
- resumeConfirmed
- WriteSecurityReport
- agent.go
- agent_loop.go
- prose_calls.go
- KIBBORG GO — Canonical Reference Implementation
- openTemp
- Kibborg — локальный ИИ-ассистент с双手 (агентный движок)
- Kibborg — правила для агентов
- brain_switch.go
- .CaptureScreenshot
- TestMain
- graphify reference: extra exports and benchmark
- graphify
- openTemp
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
- llm.go
- SETUP_HARDWARE.md
- Настройки `settings.ini`
- 2. Архитектура: слой за слоем
- executorSystemPrompt
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
- ensureBrain
- Системный промт.md
- fetchKlineRowsFrom
- webui.go
- hostIsInternal
- status_line_test.go
- План: toolchain кибербезопасности Kibborg
- installShutdownHook
- withMemory
- parseProseToolCalls
- renderSetup
- RunCommand
- Финальная проверка toolchain кибербезопасности
- applyGPUPowerLimits
- RSIReport
- models_fix_test.go
- SaveUpload
- mapReduceSummary
- sync.Mutex

## God Nodes (most connected - your core abstractions)
1. `Config` - 164 edges
2. `handleMessage()` - 73 edges
3. `Task` - 65 edges
4. `capAgentText()` - 49 edges
5. `ToolResult` - 49 edges
6. `routeWebMessage()` - 48 edges
7. `runLayeredAgent()` - 39 edges
8. `safeActor()` - 37 edges
9. `okResult()` - 35 edges
10. `failResult()` - 34 edges

## Surprising Connections (you probably didn't know these)
- `stopBrainOnPort()` --calls--> `forgetBrainProc()`  [INFERRED]
  engine-go/brain_switch.go → engine-go/shutdown.go
- `main()` --calls--> `loadHistory()`  [INFERRED]
  engine-go/main.go → engine-go/history.go
- `handleWebImageTurn()` --calls--> `imageUploadMime()`  [INFERRED]
  engine-go/webui.go → engine-go/media.go
- `handleAPIModelCancel()` --calls--> `cancelModelDownload()`  [INFERRED]
  engine-go/models_web.go → engine-go/models_hub.go
- `agentSoftBudget()` --calls--> `brainCtxSize()`  [INFERRED]
  engine-go/agent.go → engine-go/compact.go

## Import Cycles
- None detected.

## Communities (157 total, 16 thin omitted)

### Community 0 - "Session"
Cohesion: 0.15
Nodes (8): DOMSummary, SimpleCookie, Session, parseTable(), resolveURL(), encoding/json.RawMessage, github.com/PuerkitoBio/goquery.Document, github.com/PuerkitoBio/goquery.Selection

### Community 1 - "AnalyzeSymbol"
Cohesion: 0.06
Nodes (74): AnalyzeSymbol(), containsString(), countSeverity(), dataQualityStatus(), decideVerdict(), orderflowConfirm(), planInputs(), regimeHasWarning() (+66 more)

### Community 2 - "TZ.md"
Cohesion: 0.06
Nodes (33): 10. Finding Normalizer, 11. Correlation Engine, 12. Validation Engine, 13. Risk Engine, 14. Code Security Engine, 15. Browser Agent, 16. Security Memory, 17. Security Baseline (+25 more)

### Community 3 - "Task"
Cohesion: 0.05
Nodes (95): toolBudget(), CompactResult, dispatchDocumentTool(), docToolSpecs(), argString(), handsRecord, appendJSONL(), rotateJSONL() (+87 more)

### Community 4 - "SizePosition"
Cohesion: 0.07
Nodes (46): cmdArgs, gerchikSizingBlock(), BuildPlan(), invalidation(), roundPrice(), roundPriceSlice(), TestBuildPlanLong(), TestBuildPlanNonDirectional() (+38 more)

### Community 5 - "Store"
Cohesion: 0.10
Nodes (18): decodeFloats(), encodeFloats(), Open(), round2(), scanTrade(), cosine(), decodeVec(), encodeVec() (+10 more)

### Community 6 - "1.md"
Cohesion: 0.07
Nodes (29): 0. Scope и контроль безопасности, 10. Mass Assignment, 11. Business Logic, 12. API Security, 13. Injection Testing, 14. File Upload, 15. SSRF, 16. CORS / CSRF (+21 more)

### Community 7 - "Gerchik Trader QA — алгоритм Герчика: полный справочник для проверки"
Cohesion: 0.04
Nodes (46): 10. Крипто-специфика, 11. Что нельзя формализовать (субъективное), 12. Расхождения между источниками курса, 13. Процедура аудита, 14. Типичные ошибки реализации (красные флаги), 15. Формулы для сверки с кодом, 16. Что должно быть покрыто тестами, 17. Форма отчёта аудита (+38 more)

### Community 8 - "capAgentText"
Cohesion: 0.23
Nodes (19): capAgentText(), sendTelegramDocumentFile(), handleTelegramPDF(), handleWebPDFTurn(), downloadTelegramFileTo(), chartTaskText(), recordHistory(), describePlan() (+11 more)

### Community 9 - "инстументы для кибербезопасности.md"
Cohesion: 0.06
Nodes (31): 10. Containers / Kubernetes / Cloud, 11. Архитектура Киборга, 1. Recon / разведка, 2. Vulnerability Scanner, 3. Web fuzzing / discovery, 4. API Security, 5. SQL Injection, 6. Password / Credential Security (+23 more)

### Community 10 - "tts.go"
Cohesion: 0.15
Nodes (27): SpeechFile, elapsedMS(), ensureTTS(), isTTSTimeout(), prettyHost(), pulseTTSAffinity(), respreadTTSAfterFirstSynth(), sendTelegramVoiceFile() (+19 more)

### Community 11 - "engine-go/video.go"
Cohesion: 0.07
Nodes (60): convertOpts, frameShot, mediaAnalyzeOpts, mediaDigest, mediaInfo, mediaSource, mediaStream, transcriptChunk (+52 more)

### Community 12 - "runExecutorLoop"
Cohesion: 0.28
Nodes (8): agentSoftBudget(), compactToolMessages(), estimateAgentChars(), loopState, hasPack(), runExecutorLoop(), shrinkAgentMsgs(), stepBudget()

### Community 13 - "desktop_windows.go"
Cohesion: 0.10
Nodes (36): bitmapInfo, bitmapInfoHeader, blockedInputHint(), capturePNG(), captureScreenPNG(), captureWindowPNG(), clickMouse(), findWindow() (+28 more)

### Community 14 - "handleCallbackQuery"
Cohesion: 0.10
Nodes (38): currentHandsMode(), handsModeLabel(), handsModeShort(), loadHandsMode(), normalizeHandsMode(), setHandsMode(), handsModeFile, handledPreQueue() (+30 more)

### Community 15 - "models_hub.go"
Cohesion: 0.05
Nodes (75): activity, assignedPaths, findLocalWeight(), TestFindLocalWeightExactAndAmbiguous(), TestInferCapsQwen38(), catalogEntry, CPUInfo, DiskInfo (+67 more)

### Community 16 - "ScanText"
Cohesion: 0.11
Nodes (31): capSample(), classify(), looksLikeFile(), redactThreatSample(), ScanText(), sevIcon(), sevRank(), findIOC() (+23 more)

### Community 17 - "search.go"
Cohesion: 0.15
Nodes (31): enginePack, searchEngine, SearchResult, blockedReason(), chromeSearch(), cleanSearchURL(), decodeDDG(), engineBing() (+23 more)

### Community 18 - "guard.go"
Cohesion: 0.13
Nodes (38): Action, Decision, allowD(), askD(), autostartDirs(), blockD(), classifyToolCall(), commandPaths() (+30 more)

### Community 19 - "futures.go"
Cohesion: 0.15
Nodes (31): FlowBar, fundingEvent, FundingPrint, attachFlowToTimeframes(), buildCVDCandles(), buildLevelCandles(), chartFundingInterval(), fapiHostOrder() (+23 more)

### Community 20 - "chatWithHistoryStream"
Cohesion: 0.14
Nodes (21): htmlEscape(), renderEmphasis(), renderInline(), stripTags(), toTelegramHTML(), liveMessage, sendTelegramChunk(), sendTelegramRaw() (+13 more)

### Community 21 - "testing.T"
Cohesion: 0.06
Nodes (51): TestBytesHaveNUL(), TestIsTextFile(), TestRenderInlineUnmatchedBacktick(), TestStreamPreview(), TestStripThink(), TestToTelegramHTML(), TestToolFingerprintNormalizesURLAndJWT(), TestEstimateAgentChars() (+43 more)

### Community 22 - "Config"
Cohesion: 0.12
Nodes (43): Config, atofDefault(), atoiDefault(), boolDefault(), loadConfig(), embedEnabled(), embedReady(), embedText() (+35 more)

### Community 23 - "document.go"
Cohesion: 0.10
Nodes (42): docDigest, docHit, docPage, docReadOpts, collapseRuns(), docCachePath(), docLanguage(), documentReady() (+34 more)

### Community 24 - "numbers.go"
Cohesion: 0.08
Nodes (52): allDigits(), alreadySpelled(), annotateLine(), annotateNumbers(), collapseAdjacentParens(), collapseAdjacentParensOnce(), collapseDupParens(), collapseDupParensOnce() (+44 more)

### Community 25 - "Session"
Cohesion: 0.15
Nodes (7): Tab, TestSameLocationIgnoresFragmentOnly(), altLoopback(), Session, isStalePageErr(), sameLocation(), shortID()

### Community 26 - "context.Context"
Cohesion: 0.21
Nodes (25): AgentReachDoctor(), AgentReachRun(), cleanSubtitle(), excerptBody(), formatSearchWithHarvest(), GitHubSearch(), harvestPages(), harvestPriority() (+17 more)

### Community 27 - "rsi.go"
Cohesion: 0.12
Nodes (35): AnalyzeRSI(), BuildOscillatorPane(), collectOscMarks(), consecFromEnd(), crossed(), detectDivergence(), levelCross(), maxInt() (+27 more)

### Community 28 - "indicators.go"
Cohesion: 0.18
Nodes (25): timeframeData(), ATR(), EMA(), EMALast(), MACDHist(), RSI(), rsiFromAvg(), RSISeries() (+17 more)

### Community 29 - "browser/video.go"
Cohesion: 0.16
Nodes (23): VideoDownload, TestIsVideoURL(), ExtractVideoURL(), FetchMedia(), FetchVideo(), fileExists(), FormatVideoResult(), Session (+15 more)

### Community 30 - "New"
Cohesion: 0.12
Nodes (25): TestUnparsedToolCallDetected(), unparsedToolCall(), tableFromHTML(), TestActAndReadToolMapsMatchPacks(), TestAllocatorFollowsChromeRestart(), TestCapStrKeepsValidUTF8(), TestClickElementAcceptsTextAndSelector(), TestDecodeDDG() (+17 more)

### Community 31 - "gerchik_test.go"
Cohesion: 0.10
Nodes (64): AnalyzeGerchik(), avgRange(), buildLevels(), describeLevel(), gerchikATR(), globalTrend(), Bar, GerchikReport (+56 more)

### Community 32 - "history.go"
Cohesion: 0.67
Nodes (3): loadHistory(), saveHistoryNow(), startHistorySaver()

### Community 33 - "market.go"
Cohesion: 0.25
Nodes (17): candle, analyzeTicker(), anyToF(), fetchDailyBars(), fetchKlineRows(), fetchKlines(), klineHostOrder(), narrateReport() (+9 more)

### Community 34 - "status_line.go"
Cohesion: 0.26
Nodes (20): clipStatus(), firstArg(), firstNLines(), formatInt(), loopState, itoa(), jsonArrayPrefix(), linkListHint() (+12 more)

### Community 35 - "DownloadURL"
Cohesion: 0.24
Nodes (16): DownloadURL(), isBenignHTMLPath(), looksLikeHTMLBody(), looksLikeProbeTrapPath(), looksLikeSecretPath(), looksLikeSPAAppShell(), looksSQL(), looksZip() (+8 more)

### Community 36 - "Session"
Cohesion: 0.12
Nodes (4): assertPublicTabURL(), Session, enableCaptureDomains(), github.com/chromedp/chromedp.Action

### Community 37 - "What You Must Do When Invoked"
Cohesion: 0.08
Nodes (24): For /graphify add and --watch, For /graphify query, For the commit hook and native AGENTS.md integration, For --update and --cluster-only, /graphify, Honesty Rules, Interpreter guard for subcommands, Part A - Structural extraction for code files (+16 more)

### Community 38 - "What You Must Do When Invoked"
Cohesion: 0.08
Nodes (24): For /graphify add and --watch, For /graphify query, For the commit hook and native CLAUDE.md integration, For --update and --cluster-only, /graphify, Honesty Rules, Interpreter guard for subcommands, Part A - Structural extraction for code files (+16 more)

### Community 39 - "runLayeredAgent"
Cohesion: 0.09
Nodes (52): assistantText(), assistantToolCall(), checkToolProtocol(), containsAll(), newFakeBrain(), splitHostPort(), TestE2EDesktopScreenshotReachesUser(), TestE2EDispatcherGarbageFallsBackToWeb() (+44 more)

### Community 40 - "lookAtScreen"
Cohesion: 0.30
Nodes (10): lookAtScreen(), parseFindings(), renderFindings(), shrinkPNG(), TestParseFindingsMapsToScreenCoordinates(), TestParseFindingsRejectsGarbage(), TestRenderFindingsWarnsAboutPrecision(), TestShrinkPNGKeepsAspectAndContent() (+2 more)

### Community 41 - "ToolSpec"
Cohesion: 0.35
Nodes (11): ToolSpec, arrp(), boolp(), DedupTools(), Session, intp(), obj(), param() (+3 more)

### Community 42 - "compactChatHistory"
Cohesion: 0.26
Nodes (13): chatMsg, brainCtxSize(), compactChatHistory(), contextSnapshot(), estimateMsgTokens(), estimateTokens(), fetchCtxSize(), handleCompactCommand() (+5 more)

### Community 43 - "safeActor"
Cohesion: 0.18
Nodes (28): Actor, TestNonOwnerGetsDenyNotAsk(), TestExecutorPromptIsHandsAware(), guardToolCall(), ownerCheck(), fullActor(), safeActor(), TestFullModeDowngradesAskNotBlock() (+20 more)

### Community 44 - "What You Must Do When Invoked"
Cohesion: 0.08
Nodes (24): For /graphify add and --watch, For /graphify query, For the commit hook and native CLAUDE.md integration, For --update and --cluster-only, /graphify, Honesty Rules, Interpreter guard for subcommands, Part A - Structural extraction for code files (+16 more)

### Community 45 - "desktop_other.go"
Cohesion: 0.15
Nodes (11): captureScreenPNG(), captureWindowPNG(), findWindow(), findWindowAll(), focusDesktopWindow(), foregroundWindow(), desktopWindow, screenRect (+3 more)

### Community 46 - "newTestLoop"
Cohesion: 0.27
Nodes (18): answeredIDs(), call(), newTestLoop(), TestBadArgumentsAreFailure(), TestDeadToolDoesNotBecomeTheAnswer(), TestDeniedCallReportsDeniedStatus(), TestFailedActionsSurfaceToUser(), TestFinishTaskUsesSecReportWhenModelSilent() (+10 more)

### Community 47 - ".Dispatch"
Cohesion: 0.19
Nodes (16): toJSON(), DeleteLocal(), isMostlyBinary(), ListLocalDir(), LocalFileInfo(), MkdirLocal(), ReadLocalFile(), WriteLocalFile() (+8 more)

### Community 48 - "probe.go"
Cohesion: 0.28
Nodes (14): analyzeProbe(), appendSensitivePathChecks(), dash(), makeResponseFingerprint(), ProbeURL(), sameSiteName(), sameSPAShell(), tlsVersionName() (+6 more)

### Community 49 - "browser/schema.go"
Cohesion: 0.17
Nodes (8): CapturedRequest, FormField, FormInfo, ImageRef, Link, ToolFunction, WSMessage, Session

### Community 50 - ".fetchAs"
Cohesion: 0.23
Nodes (9): cloner, CloneResult, TestSanitizeNameLongExtension(), assetSubdir(), defaultExt(), download(), Session, sanitizeName() (+1 more)

### Community 51 - "README.md"
Cohesion: 0.10
Nodes (18): Kibborg Go Engine, Безопасность и «длина рук», Веб-панель, Документы рядом, Если что-то не работает, Железо и скорость, За 30 секунд, Как пользоваться (+10 more)

### Community 52 - "Handler"
Cohesion: 0.23
Nodes (7): Any, BaseHTTPRequestHandler, Handler, load_model(), _resolve_lang(), _resolve_voice(), synthesize()

### Community 53 - "newTask"
Cohesion: 0.16
Nodes (18): TestDocumentToolWired(), newTask(), newTaskID(), registerTask(), TestArtifactsNeverEnterModelContext(), TestConfirmWordParsing(), TestInterruptedNotRemembered(), TestInterruptedStatusesDiffer() (+10 more)

### Community 54 - "secops_wire.go"
Cohesion: 0.08
Nodes (44): main(), findHackerToolsFile(), hackerToolsCandidates(), LoadCatalog(), PlaybookPath(), RenderCatalogMarkdown(), ResetCatalogCacheForTest(), SearchCatalog() (+36 more)

### Community 55 - "📊 Распределение модели по слоям и памяти — Kibborg DaVinchi Bot"
Cohesion: 0.10
Nodes (19): KIBORG-LONG профиль, n-gpu-layers 99, Tensor split: 0.35, 0.65, 🔌 Блок питания, 🎮 Видеокарты, ⚡ Итог одним взглядом, Квоты контекста, 📦 Классификация файлов GGUF (по hardware.go) (+11 more)

### Community 56 - "safeRemoteURL"
Cohesion: 0.19
Nodes (15): blockInternalRedirects(), dialPublicOnly(), hostIsInternal(), ipIsInternal(), looksNonCanonicalIP(), redirectSafeClient(), safeArtifactPath(), safeHTTPClient() (+7 more)

### Community 57 - ".executeGuarded"
Cohesion: 0.32
Nodes (8): statusBrain(), statusInfo(), statusResult(), statusResultBody(), statusTool(), TestToolResultLineShowsContentNotOnlyChars(), StatusUpdate, toolHasOKQuota()

### Community 58 - "handleMessage"
Cohesion: 0.11
Nodes (48): runAgent(), sendArtifacts(), wantsToolAgent(), agentRequest, TestWebVoiceAndTextShareOneRoute(), actorFor(), isOwnerChat(), markHistoryDirty() (+40 more)

### Community 59 - "webui_test.go"
Cohesion: 0.18
Nodes (16): inputAcceptFilter(), TestAnalyzePageHasRSIOscillator(), TestLiveTokensReachTheUI(), TestModelsTabAndHardwareAPI(), TestRouteWebMessageHardwareAndAnalyze(), TestSecurityTabHasConfirmAndContext(), TestUploadAcceptCoversSupportedKinds(), TestWebCandlesValidation() (+8 more)

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

### Community 66 - "Table"
Cohesion: 0.46
Nodes (6): Table, escapeCells(), exportTable(), itoa(), tableCSV(), tableMarkdown()

### Community 67 - "resumeConfirmed"
Cohesion: 0.18
Nodes (16): getBrowserSession(), getBrowserSessionWithFFmpeg(), checkInterrupted(), finishTask(), resumeConfirmed(), agentResult, stripThink(), capMem() (+8 more)

### Community 68 - "WriteSecurityReport"
Cohesion: 0.27
Nodes (11): buildSecurityMarkdown(), inferTargetFromMarkdown(), ListSecurityReports(), sanitizeReportName(), scrubReportSecrets(), TestWriteSecurityReport(), TestWriteSecurityReport_InfersTargetFromBody(), TestWriteSecurityReport_RequiresFields() (+3 more)

### Community 69 - "agent.go"
Cohesion: 0.06
Nodes (55): agentURLBagNote(), capLog(), clearAgentURLs(), extractHTTPURLs(), getAgentURLs(), isContextBad(), isContextOverflow(), isVideoExt() (+47 more)

### Community 70 - "agent_loop.go"
Cohesion: 0.15
Nodes (14): agentBusyNotice(), apiFilesRelFromText(), claimsNoTools(), isPostReportNoise(), readNudge(), toolNamesOf(), validToolCalls(), SearchHasExcerpts() (+6 more)

### Community 71 - "prose_calls.go"
Cohesion: 0.25
Nodes (13): hasControlChars(), identBefore(), indexTopLevel(), isIdentByte(), isIdentifier(), isPlaceholder(), matchBrace(), matchParen() (+5 more)

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

### Community 77 - "brain_switch.go"
Cohesion: 0.25
Nodes (13): brainSwitchBusy(), killPID(), parsePIDList(), pidsListening(), pidsListeningUnix(), pidsListeningWindows(), runBrainSwitch(), setBrainSwitch() (+5 more)

### Community 80 - "graphify reference: extra exports and benchmark"
Cohesion: 0.22
Nodes (8): graphify reference: extra exports and benchmark, Step 6b - Wiki (only if --wiki flag), Step 7 - Neo4j export (only if --neo4j or --neo4j-push flag), Step 7a - FalkorDB export (only if --falkordb or --falkordb-push flag), Step 7b - SVG export (only if --svg flag), Step 7c - GraphML export (only if --graphml flag), Step 7d - MCP server (only if --mcp flag), Step 8 - Token reduction benchmark (only if total_words > 5000)

### Community 83 - "openTemp"
Cohesion: 0.48
Nodes (6): Store, openTemp(), TestKeywordRecall(), TestRecentEpisodesChronological(), TestSummaryRoundTripAndForget(), TestVectorRecallRanksBySimilarity()

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
Cohesion: 0.22
Nodes (8): Запрещено, Плейбук: авторизованный тест на прочность, Порядок работы, Режимы глубины, Формат находки в отчёте, Цель, Цепочки CLI (полный режим), Что проверять (минимум)

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

### Community 104 - "llm.go"
Cohesion: 0.20
Nodes (16): assistantToolMsg(), assistantMsg, applyChatSampling(), applyGenLimit(), applyToolSampling(), TestCoerceChatMessages_MergesSystems(), TestCoerceChatMessages_NoSystem(), coerceChatMessages() (+8 more)

### Community 105 - "SETUP_HARDWARE.md"
Cohesion: 0.33
Nodes (5): 1. Рекомендуемая модель (по умолчанию), 2. Прямые ссылки, 3. Команда для скачивания, 4. Полная команда запуска llama-server (оптимизировано под dual 3060), 5. Полезные советы

### Community 106 - "Настройки `settings.ini`"
Cohesion: 0.33
Nodes (6): Голос и видео, Каналы, Мозг, Настройки `settings.ini`, Память и трейдер, Руки

### Community 107 - "2. Архитектура: слой за слоем"
Cohesion: 0.40
Nodes (5): 2. Архитектура: слой за слоем, Слой 1 — Диспетчер (`dispatcher.go`), Слой 2 — Сбор рук (`packs.go`), Слой 3 — Ворота / Guard (`guard.go`), Слой 4 — Исполнение (`agent_loop.go`)

### Community 108 - "executorSystemPrompt"
Cohesion: 0.20
Nodes (10): agentSystemPrompt(), armouryNote(), executorSystemPrompt(), TestExecutorPromptIsPackAware(), TestFallbackHonoursHint(), TestParseDispatchJSON(), TestPlanAndSummaryHaveConsumers(), TestWebPackAloneForcesRead() (+2 more)

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

### Community 138 - "ensureBrain"
Cohesion: 0.21
Nodes (12): brainLogPath(), brainServerArgs(), ensureBrain(), llamaProcEnv(), llamaThreadCount(), orDash(), TestBrainServerArgs256KNoKVOffload(), TestBrainServerArgsKeepsKVOnGPUByDefault() (+4 more)

### Community 139 - "Системный промт.md"
Cohesion: 0.50
Nodes (3): 1. Успешный доступ к чувствительным данным (Критическая уязвимость), 2. Недоступные эндпоинты (404 (четыреста четыре) / ROUTE_NOT_FOUND), 3. Вывод

### Community 140 - "fetchKlineRowsFrom"
Cohesion: 0.27
Nodes (12): fetchKlineRowsFrom(), getKlineRows(), isUnknownBinanceSymbol(), parseKlineRows(), rememberKlineHost(), sampleKlineJSON(), TestKlineAllHostsFail(), TestKlineFallbackUsesSecondHost() (+4 more)

### Community 141 - "webui.go"
Cohesion: 0.09
Nodes (62): localModelCards(), startBrainSwitch(), switchSnapshot(), TestAgentControlEndpointsAreCSRFGuarded(), TestHandsEndpointReadsAndWrites(), TestSameOrigin_BlocksCrossSiteAndRebinding(), TestSameOriginGuard_Returns403(), TestStopEndpointWithoutTask() (+54 more)

### Community 142 - "hostIsInternal"
Cohesion: 0.21
Nodes (11): dialPublicOnly(), hostIsInternal(), ipIsInternal(), looksNonCanonicalIP(), safePublicURL(), TestProbeURL_FindsMissingHeaders(), TestSafePublicURL_AllowsPublic(), TestSafePublicURL_BlocksPrivate() (+3 more)

### Community 143 - "status_line_test.go"
Cohesion: 0.15
Nodes (15): toolParallelOK(), tagToolName(), TestFirstNLines(), TestPageTitleHintSkipsMeta(), TestProbeURLStatusShowsHostAndFindings(), TestSearchHitHintFromJSON(), TestThinkLineNamesWhatWasRead(), TestToolParallelOKList() (+7 more)

### Community 144 - "План: toolchain кибербезопасности Kibborg"
Cohesion: 0.15
Nodes (12): MISS на native Windows (честно), OK (в PATH / wrappers `%USERPROFILE%\go\bin`), Инвентарь на старте (2026-08-27), Итог установки (после прогона), Лог выполнения, Нет в PATH (поставить), План: toolchain кибербезопасности Kibborg, Словари (+4 more)

### Community 145 - "installShutdownHook"
Cohesion: 0.22
Nodes (10): closeBrowserSession(), brainKillSet(), closeMemory(), forgetBrainProc(), installShutdownHook(), registerEngineProc(), stopLaunchedEngines(), trackBrainProc() (+2 more)

### Community 146 - "withMemory"
Cohesion: 0.13
Nodes (28): bytesHaveNUL(), chatWithFile(), isTextFile(), chatWithImage(), describeImage(), describeImageBytes(), downloadTelegramFile(), extractChartTicker() (+20 more)

### Community 147 - "parseProseToolCalls"
Cohesion: 0.45
Nodes (10): parseProseToolCalls(), argsOf(), proseTools(), TestParseJSONProseToolCall(), TestParseProseCallFromLiveRun(), TestParseProseFormsAndTypes(), TestParseProseKeepsUnescapedPath(), TestParseProseNoArgTool() (+2 more)

### Community 148 - "renderSetup"
Cohesion: 0.33
Nodes (9): flowQuadName(), renderFlow(), absF(), renderGerchik(), renderSetup(), round4(), signHalf(), takeNote() (+1 more)

### Community 149 - "RunCommand"
Cohesion: 0.27
Nodes (9): capCmdBytes(), exitCode(), RunCommand(), TestCleanSubtitle(), TestRunCommandCancelKillsChild(), TestRunCommandEcho(), TestRunCommandEmpty(), TestRunCommandTaskDeadlineWins() (+1 more)

### Community 150 - "Финальная проверка toolchain кибербезопасности"
Cohesion: 0.22
Nodes (8): 1. Инвентарь CLI (`tools.json` ↔ PATH), 2. Smoke `--version` / help (без атак по сети), 3. Логика Kibborg, 4. Как это должно работать в UI, 5. Ограничения (честно), 6. Действие пользователя, Вердикт, Финальная проверка toolchain кибербезопасности

### Community 151 - "applyGPUPowerLimits"
Cohesion: 0.33
Nodes (8): applyGPUPowerLimits(), parseGPUPowerLimits(), parseWatts(), TestLoadConfigGPUPowerLimit(), TestParseGPUPowerLimits(), TestParseGPUPowerLimitsRejectsGarbage(), TestParseWattsReadsNvidiaSMIFormat(), gpuPowerRule

### Community 152 - "RSIReport"
Cohesion: 0.27
Nodes (8): renderRSI(), rsiCrossLine(), rsiScenarioName(), TestRenderFlowBlock(), TestRenderReportIncludesRSI(), TestRenderRSIIsFilterNotEntry(), RSIReport, rsiVerdict()

### Community 153 - "models_fix_test.go"
Cohesion: 0.40
Nodes (5): containsInt(), TestAssignBrainModelRejectsNonGGUF(), TestBrainKillSetScopedToBrainAndPort(), TestLiveBrainModelAtomic(), setLiveBrainModel()

### Community 154 - "SaveUpload"
Cohesion: 0.47
Nodes (4): AttachmentBrief(), SaveUpload(), TestSaveUploadWritesUnderUploads(), uploadHasNUL()

### Community 155 - "mapReduceSummary"
Cohesion: 0.60
Nodes (4): chunkText(), isSpaceRune(), mapReduceSummary(), TestChunkTextKeepsEverything()

### Community 156 - "sync.Mutex"
Cohesion: 0.50
Nodes (4): brainSwitchState, modelDownload, context.CancelFunc, sync.Mutex

## Knowledge Gaps
- **473 isolated node(s):** `C:/Users/lex66/AppData/Roaming/uv/tools/graphifyy/Scripts/python.exe`, `Session`, `Session`, `keybdInput`, `mouseInput` (+468 more)
  These have ≤1 connection - possible missing edges or undocumented components.
- **16 thin communities (<3 nodes) omitted from report** — run `graphify query` to explore isolated nodes.

## Suggested Questions
_Questions this graph is uniquely positioned to answer:_

- **Why does `Config` connect `Config` to `Task`, `SizePosition`, `capAgentText`, `ensureBrain`, `tts.go`, `runExecutorLoop`, `webui.go`, `handleCallbackQuery`, `engine-go/video.go`, `installShutdownHook`, `withMemory`, `chatWithHistoryStream`, `document.go`, `applyGPUPowerLimits`, `mapReduceSummary`, `market.go`, `runLayeredAgent`, `lookAtScreen`, `compactChatHistory`, `handleMessage`, `resumeConfirmed`, `agent.go`, `brain_switch.go`, `llm.go`?**
  _High betweenness centrality (0.067) - this node is a cross-community bridge._
- **Why does `Task` connect `Task` to `resumeConfirmed`, `agent.go`, `agent_loop.go`, `runLayeredAgent`, `capAgentText`, `safeActor`, `runExecutorLoop`, `engine-go/video.go`, `sync.Mutex`, `newTask`, `.executeGuarded`, `context.Context`, `tool_args.go`, `gerchik_test.go`?**
  _High betweenness centrality (0.031) - this node is a cross-community bridge._
- **Why does `handleMessage()` connect `handleMessage` to `market.go`, `SizePosition`, `runLayeredAgent`, `capAgentText`, `compactChatHistory`, `tts.go`, `webui.go`, `handleCallbackQuery`, `models_hub.go`, `withMemory`, `chatWithHistoryStream`, `Config`, `secops_wire.go`, `browser/video.go`?**
  _High betweenness centrality (0.030) - this node is a cross-community bridge._
- **Are the 48 inferred relationships involving `handleMessage()` (e.g. with `runLayeredAgent()` and `wantsToolAgent()`) actually correct?**
  _`handleMessage()` has 48 INFERRED edges - model-reasoned connections that need verification._
- **What connects `C:/Users/lex66/AppData/Roaming/uv/tools/graphifyy/Scripts/python.exe`, `Session`, `Session` to the rest of the system?**
  _473 weakly-connected nodes found - possible documentation gaps or missing edges._
- **Should `AnalyzeSymbol` be split into smaller, more focused modules?**
  _Cohesion score 0.05910364145658263 - nodes in this community are weakly interconnected._
- **Should `TZ.md` be split into smaller, more focused modules?**
  _Cohesion score 0.058823529411764705 - nodes in this community are weakly interconnected._