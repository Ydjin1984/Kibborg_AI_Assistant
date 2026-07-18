package main

// Kibborg — minimal Telegram ↔ LLM bridge.
// Two jobs only: (1) launch the local LLM (llama-server) and (2) let the user chat
// with it through Telegram. All other tooling was intentionally removed.

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"kibborg/engine/browser"
)

const systemPrompt = `Ты — Kibborg, продвинутый локальный ИИ-ассистент в Telegram (текст, картинки, код, анализ). Отвечай по-русски.

ОФОРМЛЕНИЕ (строго в Markdown, НЕ пиши HTML-теги):
- **жирный** — для заголовков секций и ключевых акцентов.
- ` + "`инлайн-код`" + ` — для имён файлов, команд, параметров и числовых значений.
- Блоки кода и многострочные данные — только в ограждённых блоках с указанием языка:
  ` + "```python" + `
  код тут
  ` + "```" + `
- Разделяй смысловые секции строкой-разделителем: ━━━━━━━━━━━━━━━
- Заголовки секций — с эмодзи-иконкой (🎯 🔍 💻 ⚠️ 📋 👀 🛠).
- Где уместна важность/статус — маркеры 🟢 норма · 🟡 внимание · 🔴 критично, итоги — с ✅.

СТРУКТУРА (масштабируй под запрос, лишние секции опускай):
- Короткий вопрос → короткий ответ в 1–3 предложения, без тяжёлых блоков и разделителей.
- Сложный/длинный ответ → секции в порядке (только нужные):
  🎯 **Кратко** — суть одним абзацем
  🔍 **Подробно** — детали списком
  💻 **Код** — в блоке с языком
  ⚠️ **Важно** — ограничения и риски
  📋 **Итог** — выводы списком с ✅
- Не делай «стену текста»: короткие абзацы, списки, разделители между секциями.

Если чего-то не знаешь или не уверен — честно скажи. Код всегда в блоках, чтобы копировался одним нажатием.`

// chartSystemPrompt is the role used ONLY for /chart (and /analyze) image requests.
// It turns the model into a strict "trade filter": its job is to REJECT weak setups,
// not to invent a trade. Numbers must be read off the chart, never made up. The output
// format is fixed — the model fills the template verbatim, no freeform prose.
const chartSystemPrompt = `Ты — профессиональный трейдер и риск-менеджер с опытом на крипто- и фондовых рынках. Перед тобой скриншот графика (TradingView и т.п.).

Ты НЕ аналитик, который ищет сделку любой ценой, а ТОРГОВЫЙ ФИЛЬТР: твоя задача не искать сделки, а отбраковывать плохие.

ПРАВИЛА ФИЛЬТРА — выбирай WAIT, если выполняется хотя бы одно:
- вероятность успеха ниже 70%;
- стоп-лосс дальше 5% от входа;
- риск/прибыль хуже 1:2;
- структура рынка неоднозначна;
- ключевые уровни неочевидны.
Запрещено выдавать торговый сигнал без достаточных оснований. Никогда не выдумывай числа, которых нет на графике — все цены бери прямо с картинки. Если инструмент, таймфрейм или цену не видно — напиши об этом и выбери WAIT.

Сначала (про себя, НЕ выводя в ответ) пройди чек-лист: инструмент, таймфрейм, текущая цена, тренд старшей структуры, структура (HH/HL/LH/LL), уровни поддержки/сопротивления, зоны ликвидности, ложные пробои, импульсы и коррекции, вероятности продолжения / разворота / флэта.

ФОРМАТ ОТВЕТА — выводи СТРОГО по шаблону ниже и НИЧЕГО лишнего: без вступлений, без нумерованных списков, без пояснений вне шаблона.

Если вердикт WAIT — выводи РОВНО это:

⚪ WAIT

Причины:
- <короткий пункт>
- <короткий пункт>

Если вердикт LONG или SHORT — выводи РОВНО это (🟢 для LONG, 🔴 для SHORT):

🟢 LONG

Направление: LONG
Вероятность: NN%
Вход: <цена>
DCA1: <цена>
DCA2: <цена>
SL: <цена>
TP1: <цена>
TP2: <цена>
TP3: <цена>
R:R: 1:N

Причины ЗА:
- <пункт>
- <пункт>

Причины ПРОТИВ:
- <пункт>
- <пункт>

Финальный вердикт: LONG

Отвечай по-русски. Лучше честный WAIT, чем выдуманная сделка.`

// chartCommands (text) start the "send me a chart screenshot" flow.
var chartCommands = []string{"/chart", "/график"}

// analyzeCommands (text) run the deterministic ticker analysis over live Binance data.
var analyzeCommands = []string{"/analyze", "/анализ", "/разбор"}

const maxHistory = 12 // sliding window of recent user/assistant messages per chat

var (
	histMu  sync.Mutex
	history = map[int64][]chatMsg{}
)

// chartPending tracks chats that sent /chart as a standalone command and are now
// expected to send a chart image/file next. The stored string is any extra context
// the user typed after /chart. Presence of the key (not its value) means "pending".
var (
	chartMu      sync.Mutex
	chartPending = map[int64]string{}
)

func setChartPending(chatID int64, extra string) {
	chartMu.Lock()
	chartPending[chatID] = extra
	chartMu.Unlock()
}

// takeChartPending returns the stored context and whether a chart was pending, clearing it.
func takeChartPending(chatID int64) (string, bool) {
	chartMu.Lock()
	defer chartMu.Unlock()
	extra, ok := chartPending[chatID]
	if ok {
		delete(chartPending, chatID)
	}
	return extra, ok
}

func main() {
	cfg := loadConfig("settings.ini")
	if cfg.TelegramToken == "" {
		log.Fatal("TELEGRAM_TOKEN не задан (ни в settings.ini, ни в env)")
	}
	// Remember the token so redact() can strip it from any error/log line before it leaks.
	secretToken = cfg.TelegramToken
	// Launch the LLM in the background; chat waits for it to become ready.
	go ensureBrain(cfg)
	// Preload the model into VRAM right after it's ready, so the first user request is
	// fast instead of paying the lazy KV/compute/kernel allocation cost on first message.
	go warmUpBrain(cfg)
	// Speech recognition (whisper.cpp) — only if configured in settings.ini.
	go ensureWhisper(cfg)
	// Dialog context survives restarts.
	loadHistory()
	startHistorySaver()
	// Long-term memory (SQLite + optional embeddings). Must init before the bot accepts
	// messages so recordHistory can store exchanges. Embed server (if configured) loads async.
	initMemory(cfg)
	go ensureEmbed(cfg)
	// Trade journal (SQLite) for /log, /journal, /close and /size sizing history.
	initJournal()
	// Ctrl+C / SIGTERM: flush history, detach from Chrome, optionally stop the engines.
	installShutdownHook(cfg)
	// Local dashboard (chat + /analyze + stack status) on 127.0.0.1.
	go startWebUI(cfg)
	log.Printf("Kibborg LLM bot — brain port %d, ожидаю загрузку модели в фоне", cfg.BrainPort)
	startTelegramBot(cfg)
}

func startTelegramBot(cfg Config) {
	botAPI := "https://api.telegram.org/bot" + cfg.TelegramToken
	allow := parseAllow(cfg.TelegramID)
	log.Printf("[TELEGRAM] polling started (allowlist: %v)", allow != nil)

	offset := 0
	for {
		updates, err := getTelegramUpdates(botAPI, offset)
		if err != nil {
			log.Printf("[TELEGRAM] poll error: %v", err)
			time.Sleep(3 * time.Second)
			continue
		}
		for _, u := range updates {
			// offset must be update_id+1, else Telegram re-delivers the same update forever.
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}
			if u.Message == nil || u.Message.Chat.ID == 0 {
				continue
			}
			msg := u.Message
			log.Printf("[TELEGRAM] from %d: %s", msg.Chat.ID, strings.TrimSpace(msg.Text))
			if allow != nil && !allow[msg.Chat.ID] {
				sendTelegramMessage(botAPI, msg.Chat.ID, "⛔ Доступ к этому боту ограничён.")
				continue
			}
			enqueueMessage(cfg, botAPI, allow, msg)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// ==================== per-chat workers ====================

// Messages are handled by one serial worker per chat: replies inside a chat keep their
// order, while a long LLM answer for one chat no longer blocks every other chat.
const chatQueueCap = 8

// workerIdleTimeout reaps a chat's worker after this much silence. Without it a bot open to
// many chats accumulates one goroutine + channel per chat id forever; a new message just
// re-spawns the worker.
const workerIdleTimeout = 15 * time.Minute

var (
	workersMu sync.Mutex
	workers   = map[int64]chan *tgMessage{}
)

// enqueueMessage hands the message to its chat's worker, creating the worker on first use.
// A full queue answers immediately instead of silently dropping the message.
func enqueueMessage(cfg Config, botAPI string, allow map[int64]bool, msg *tgMessage) {
	chatID := msg.Chat.ID
	workersMu.Lock()
	q, ok := workers[chatID]
	if !ok {
		q = make(chan *tgMessage, chatQueueCap)
		workers[chatID] = q
		go chatWorker(cfg, botAPI, allow, chatID, q)
	}
	// Send under the SAME lock the reaper takes, so a worker can't be reaped between our map
	// lookup and our send (which would drop the message into a channel nobody reads).
	select {
	case q <- msg:
		workersMu.Unlock()
	default:
		workersMu.Unlock()
		sendTelegramMessage(botAPI, chatID, "⏳ Слишком много сообщений подряд — отвечаю по порядку, подожди немного.")
	}
}

// chatWorker serially handles one chat's messages, then self-reaps after workerIdleTimeout of
// silence — deleting itself from the workers map (under workersMu, so enqueue re-spawns it
// cleanly instead of sending into a dead channel).
func chatWorker(cfg Config, botAPI string, allow map[int64]bool, chatID int64, q chan *tgMessage) {
	idle := time.NewTimer(workerIdleTimeout)
	defer idle.Stop()
	for {
		select {
		case m := <-q:
			if !idle.Stop() {
				select { // drain a fired timer before Reset
				case <-idle.C:
				default:
				}
			}
			idle.Reset(workerIdleTimeout)
			safeHandleMessage(cfg, botAPI, allow, m)
		case <-idle.C:
			workersMu.Lock()
			if len(q) == 0 { // re-check under the lock: a message may have raced in
				delete(workers, chatID)
				workersMu.Unlock()
				return
			}
			workersMu.Unlock()
			idle.Reset(workerIdleTimeout)
		}
	}
}

// safeHandleMessage isolates a panic to the one message that caused it: the worker
// goroutine (and the rest of the bot) survives.
func safeHandleMessage(cfg Config, botAPI string, allow map[int64]bool, msg *tgMessage) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[TELEGRAM] panic handling message from %d: %v", msg.Chat.ID, r)
			sendTelegramMessage(botAPI, msg.Chat.ID, "❌ Внутренняя ошибка при обработке сообщения. Попробуй ещё раз.")
		}
	}()
	handleMessage(cfg, botAPI, allow, msg)
}

// handleMessage processes one Telegram message end-to-end (routing, LLM, reply).
func handleMessage(cfg Config, botAPI string, allow map[int64]bool, msg *tgMessage) {
	chatID := msg.Chat.ID
	text := strings.TrimSpace(msg.Text)

	// Image (screenshot/photo/image-file) → vision chat.
	if fileID, ok := imageFileID(msg); ok {
		if !brainReady(cfg.BrainPort) {
			sendTelegramMessage(botAPI, chatID, "⏳ Модель ещё грузится. Пришли картинку чуть позже.")
			return
		}
		prompt := strings.TrimSpace(msg.Caption)
		// Chart analysis triggers either from a /chart caption OR from a prior
		// standalone /chart command (pending state). Always consume the pending
		// flag so it doesn't leak into the next, unrelated image.
		pendExtra, pending := takeChartPending(chatID)
		chartReq, extra := parseChartCommand(prompt)
		if chartReq {
			if pending && pendExtra != "" {
				extra = strings.TrimSpace(pendExtra + " " + extra)
			}
		} else if pending {
			chartReq = true
			extra = strings.TrimSpace(pendExtra + " " + prompt)
		}
		sysPrompt := ""
		if chartReq {
			sysPrompt = chartSystemPrompt
			prompt = "Проанализируй этот график по своим правилам и дай вердикт строго по шаблону."
			if extra != "" {
				prompt += "\nДополнительный контекст от пользователя: " + extra
			}
		} else if prompt == "" {
			prompt = "Что на этом изображении? Опиши по делу."
		}
		stop := startTyping(botAPI, chatID)
		defer stop() // safety net; explicit stop() below clears it before the reply is sent
		reply := chatWithImage(cfg, chatID, prompt, fileID, sysPrompt)
		stop()
		sendTelegramMessage(botAPI, chatID, reply)
		log.Printf("[TELEGRAM] replied to %d (image, %d chars)", chatID, len(reply))
		return
	}

	// Voice / audio → Whisper transcription, then the normal chat pipeline.
	if v := firstAudio(msg); v != nil {
		handleVoiceMessage(cfg, botAPI, chatID, v.FileID, v.Duration, v.MimeType)
		return
	}

	// Document (code/text file) → read it and answer.
	if msg.Document != nil {
		// Security tools work on ANY file (incl. binaries): /scan, /audit, /logs in the caption.
		if mode, ok := parseSecFileCaption(msg.Caption); ok {
			handleSecDocument(cfg, botAPI, chatID, msg.Document, mode)
			return
		}
		if !isTextFile(msg.Document.FileName, msg.Document.MimeType) {
			sendTelegramMessage(botAPI, chatID, "❌ Этот формат пока не читаю. Шлю текст и код: .go .py .md .txt .json .yaml .sql .sh и т.п. (и картинки).")
			return
		}
		if !brainReady(cfg.BrainPort) {
			sendTelegramMessage(botAPI, chatID, "⏳ Модель ещё грузится. Пришли файл чуть позже.")
			return
		}
		prompt := strings.TrimSpace(msg.Caption)
		if prompt == "" {
			prompt = "Разбери этот файл: что он делает, есть ли проблемы и что можно улучшить."
		}
		stop := startTyping(botAPI, chatID)
		reply := chatWithFile(cfg, chatID, prompt, msg.Document)
		stop()
		sendTelegramMessage(botAPI, chatID, reply)
		log.Printf("[TELEGRAM] replied to %d (file %s, %d chars)", chatID, msg.Document.FileName, len(reply))
		return
	}

	if text == "" {
		sendTelegramMessage(botAPI, chatID, "Понимаю текст, голосовые, картинки и файлы (код/.md/.txt и т.п.).")
		return
	}

	lower := strings.ToLower(text)
	isChart, chartExtra := parseCommand(text, chartCommands)
	isAnalyze, analyzeArg := parseCommand(text, analyzeCommands)
	isBrowser, browserTask := parseBrowserCommand(text)
	isDownload, downloadURL := parseCommand(text, downloadCommands)
	isSize, sizeArg := parseCommand(text, sizeCommands)
	isLog, logArg := parseCommand(text, logCommands)
	isJournal, journalArg := parseCommand(text, journalCommands)
	isClose, closeArg := parseCommand(text, closeCommands)
	isLogs, logsArg := parseCommand(text, logsCommands)
	isScan, scanArg := parseCommand(text, scanCommands)
	isAudit, _ := parseCommand(text, auditCommands)
	switch {
	case strings.HasPrefix(lower, "/start"), strings.HasPrefix(lower, "/help"):
		sendTelegramMessage(botAPI, chatID,
			"Kibborg на связи. Просто пиши сообщение (или голосовое) — я отвечаю через локальную LLM.\n"+
				"📊 /analyze <тикер> — детерминированный разбор по данным Binance (режим, скор, тренды). Пример: /analyze BTC\n"+
				"📈 /chart — торговый разбор графика: отправь команду, затем пришли скриншот или файл графика.\n"+
				"📐 /size — размер позиции по риску. Пример: /size BTC entry=50000 stop=49000 tp=53000 risk=1.5 lev=10\n"+
				"📝 /log — записать сделку в журнал · /journal — статистика и список · /close <id> <цена> — закрыть сделку\n"+
				"🛡 /logs — анализ логов (по умолчанию свои: аномалии, всплески ошибок, утечки секретов) · /scan <текст> — поиск IOC и сигнатур атак · /audit — хеш+энтропия файла (пришли документом с подписью)\n"+
				"🌐 /browser <задача> — управлять открытым Chrome: собрать данные из DOM/сети, заполнить форму, клонировать сайт. Нужен Chrome с --remote-debugging-port=9222.\n"+
				"🎬 /download <url> — скачать видео (YouTube, Instagram, TikTok…) в макс. качестве. Можно просто прислать ссылку.\n"+
				"🧠 Я помню прошлые диалоги (долговременная память) и подмешиваю нужное в ответ.\n"+
				"/reset — очистить контекст диалога и долговременную память.")
		return
	case isDownload || browser.IsVideoURL(text) || (browser.ExtractVideoURL(text) != "" && looksLikeDownloadRequest(text)):
		url := downloadURL
		if url == "" {
			url = browser.ExtractVideoURL(text)
		}
		if url == "" {
			url = strings.TrimSpace(text)
		}
		if url == "" || (!browser.IsVideoURL(url) && browser.ExtractVideoURL(url) == "") {
			sendTelegramMessage(botAPI, chatID,
				"🎬 Укажи ссылку: /download https://youtube.com/watch?v=…\n"+
					"Поддерживаются YouTube, Instagram, TikTok, X и др. (через yt-dlp).")
			return
		}
		if extracted := browser.ExtractVideoURL(url); extracted != "" {
			url = extracted
		}
		handleVideoDownload(cfg, botAPI, chatID, url)
		return
	case isAnalyze:
		if analyzeArg == "" {
			sendTelegramMessage(botAPI, chatID,
				"📊 Укажи тикер: /analyze BTC (или ETH, SOL, BTCUSDT…). Разбор идёт по свечам Binance 15m/1h/4h.\n"+
					"Для разбора СКРИНШОТА графика используй /chart.")
			return
		}
		stop := startTyping(botAPI, chatID)
		defer stop() // idempotent (once.Do) — a safety net so the typing indicator never leaks
		report, err := analyzeTicker(analyzeArg)
		if err != nil {
			stop()
			sendTelegramMessage(botAPI, chatID, "❌ "+err.Error())
			return
		}
		// 1) deterministic report (source of truth) + position sizing, then 2) an LLM narration.
		sendTelegramMessage(botAPI, chatID, renderReport(report)+sizingBlock(cfg, report))
		log.Printf("[ANALYZE] %s → %s/%s for %d", report.Symbol, report.Regime, report.Direction, chatID)
		if brainReady(cfg.BrainPort) {
			lm := &liveMessage{botAPI: botAPI, chatID: chatID, stopTyping: stop}
			narration, stats, nerr := narrateReport(cfg, report, "разбор "+report.Symbol, lm.update)
			if nerr == nil && strings.TrimSpace(narration) != "" {
				lm.finish("🧠 " + narration + statsFooter(stats))
			} else if lm.msgID != 0 {
				lm.finish("🧠 " + narration) // salvage whatever streamed
			} else {
				stop() // nothing to show — just drop the typing indicator
			}
		} else {
			stop()
		}
		return
	case isBrowser:
		// The browser agent drives the user's REAL logged-in Chrome (clicks, forms, cookies,
		// downloads) — with an open bot that's remote control for strangers. Require the
		// allowlist explicitly.
		if allow == nil {
			sendTelegramMessage(botAPI, chatID,
				"🔒 /browser отключён, пока не задан TELEGRAM_ID в settings.ini: браузер-агент управляет твоим реальным Chrome, и открытый доступ к нему опасен. Впиши свой chat id и перезапусти бота.")
			return
		}
		if !brainReady(cfg.BrainPort) {
			sendTelegramMessage(botAPI, chatID, "⏳ Модель ещё грузится. Повтори запрос к браузеру чуть позже.")
			return
		}
		if browserTask == "" {
			sendTelegramMessage(botAPI, chatID,
				"🌐 Напиши задачу: /browser <что сделать>.\nПример: /browser собери таблицу с открытой страницы в CSV.\n(Chrome должен быть запущен с --remote-debugging-port=9222, нужная вкладка открыта.)")
			return
		}
		stop := startTyping(botAPI, chatID)
		defer stop() // safety net; explicit stop() below clears it before the reply is sent
		reply, artifacts := runBrowserAgent(cfg, browserTask)
		stop()
		sendTelegramMessage(botAPI, chatID, reply)
		sendArtifacts(botAPI, chatID, artifacts)
		log.Printf("[BROWSER] replied to %d (%d chars, %d artifacts)", chatID, len(reply), len(artifacts))
		return
	case isSize:
		sendTelegramMessage(botAPI, chatID, handleSizeCommand(cfg, sizeArg))
		return
	case isLog:
		sendTelegramMessage(botAPI, chatID, handleLogCommand(cfg, chatID, logArg))
		return
	case isJournal:
		sendTelegramMessage(botAPI, chatID, handleJournalCommand(chatID, journalArg))
		return
	case isClose:
		sendTelegramMessage(botAPI, chatID, handleCloseCommand(closeArg))
		return
	case isLogs:
		// /logs reads local files. Self-audit (no arg) is safe; a named path could exfiltrate,
		// so gate that behind the allowlist (like /browser).
		if strings.TrimSpace(logsArg) != "" && allow == nil {
			sendTelegramMessage(botAPI, chatID,
				"🔒 Анализ произвольного файла доступен только из allowlist (задай TELEGRAM_ID). Без него доступен само-аудит логов бота: `/logs`")
			return
		}
		stop := startTyping(botAPI, chatID)
		reply := handleLogsCommand(cfg, logsArg)
		stop()
		sendTelegramMessage(botAPI, chatID, reply)
		return
	case isScan:
		stop := startTyping(botAPI, chatID)
		reply := handleScanCommand(cfg, scanArg)
		stop()
		sendTelegramMessage(botAPI, chatID, reply)
		return
	case isAudit:
		sendTelegramMessage(botAPI, chatID,
			"🧬 Пришли файл документом с подписью `/audit` — посчитаю SHA256/SHA1/MD5, энтропию и тип (детект упаковки/шифрования). Для скана текста на IOC — `/scan <текст>`.")
		return
	case isChart:
		setChartPending(chatID, chartExtra)
		sendTelegramMessage(botAPI, chatID,
			"📈 Пришлите скриншот или файл графика — проанализирую и дам торговый разбор (направление, вход, усреднения, стоп, тейки).")
		return
	case strings.HasPrefix(lower, "/reset"):
		histMu.Lock()
		delete(history, chatID)
		markHistoryDirty()
		histMu.Unlock()
		takeChartPending(chatID)
		forgetMemory(chatID)
		sendTelegramMessage(botAPI, chatID, "Контекст диалога и долговременная память очищены.")
		return
	}

	if !brainReady(cfg.BrainPort) {
		sendTelegramMessage(botAPI, chatID,
			"⏳ Модель ещё грузится в память (1-5 минут после старта). Попробуй чуть позже.")
		return
	}

	// Plain text turn: the answer streams into a live-updating message (see stream.go).
	stop := startTyping(botAPI, chatID)
	chatWithHistoryStream(cfg, botAPI, chatID, text, stop)
	stop()
}

// parseCommand reports whether text opens with one of cmds and returns the rest of the line.
// Accepts Telegram group form /command@BotUsername (case-insensitive on the command token).
func parseCommand(text string, cmds []string) (bool, string) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false, ""
	}
	// First token may be "/download@MyBot" — strip @bot before matching.
	firstEnd := strings.IndexAny(trimmed, " \t\n")
	first := trimmed
	rest := ""
	if firstEnd >= 0 {
		first = trimmed[:firstEnd]
		rest = strings.TrimSpace(trimmed[firstEnd+1:])
	}
	token := strings.ToLower(first)
	if at := strings.IndexByte(token, '@'); at > 0 {
		token = token[:at]
	}
	for _, cmd := range cmds {
		if token == strings.ToLower(cmd) {
			return true, rest
		}
	}
	return false, ""
}

// parseChartCommand handles IMAGE CAPTIONS: on a picture, both /chart and /analyze mean
// "analyze this chart screenshot".
func parseChartCommand(caption string) (bool, string) {
	if ok, extra := parseCommand(caption, chartCommands); ok {
		return true, extra
	}
	return parseCommand(caption, analyzeCommands)
}

// browserCommands open the Browser Agent: the rest of the line is the natural-language task.
var browserCommands = []string{"/browser", "/браузер", "/web"}

// downloadCommands fetch a video URL at best quality (shared with the web UI).
var downloadCommands = []string{"/download", "/скачать", "/dl", "/video"}

// parseBrowserCommand reports whether text opens with a browser command and returns the task.
func parseBrowserCommand(text string) (bool, string) {
	return parseCommand(text, browserCommands)
}

// looksLikeDownloadRequest is true when free text both contains a video URL and asks to save it
// (so ordinary "посмотри https://youtu.be/…" chat is not hijacked into a download).
func looksLikeDownloadRequest(text string) bool {
	l := strings.ToLower(text)
	keys := []string{"скач", "download", "сохрани", "сохранить", "save", "dl ", "загрузи", "сгрузить"}
	for _, k := range keys {
		if strings.Contains(l, k) {
			return true
		}
	}
	return false
}

// handleVideoDownload runs yt-dlp and delivers the file to Telegram (or a local path note).
func handleVideoDownload(cfg Config, botAPI string, chatID int64, url string) {
	stop := startTyping(botAPI, chatID)
	defer stop()
	sendTelegramMessage(botAPI, chatID, "🎬 Скачиваю видео в максимальном доступном качестве…")
	res, err := browser.FetchVideo(url, cfg.FfmpegPath)
	if err != nil {
		sendTelegramMessage(botAPI, chatID, "❌ Не удалось скачать видео:\n"+err.Error())
		return
	}
	mb := float64(res.Bytes) / (1 << 20)
	title := res.Title
	if title == "" {
		title = filepath.Base(res.Path)
	}
	sendTelegramMessage(botAPI, chatID,
		fmt.Sprintf("✅ **%s**\n📦 %.1f МБ · `%s`\n📁 `%s`", title, mb, res.Ext, res.Path))
	// Try to attach the file in chat (Bot API ~50 MB limit handled inside sendArtifacts).
	sendArtifacts(botAPI, chatID, []string{res.Path})
	log.Printf("[VIDEO] %s → %s (%.1f MB) for %d", url, res.Path, mb, chatID)
}

// baseMessages returns the default system prompt + the chat's text history.
func baseMessages(chatID int64) []map[string]any {
	return baseMessagesWith(chatID, "")
}

// baseMessagesWith is like baseMessages but lets the caller override the system prompt
// (e.g. the trading-analyst role for /chart). Empty sysPrompt → default systemPrompt.
func baseMessagesWith(chatID int64, sysPrompt string) []map[string]any {
	if sysPrompt == "" {
		sysPrompt = systemPrompt
	}
	histMu.Lock()
	defer histMu.Unlock()
	prior := history[chatID]
	msgs := make([]map[string]any, 0, len(prior)+2)
	msgs = append(msgs, map[string]any{"role": "system", "content": sysPrompt})
	for _, m := range prior {
		msgs = append(msgs, map[string]any{"role": m.Role, "content": m.Content})
	}
	return msgs
}

// recordHistory appends a user/assistant exchange, trimmed to the sliding window.
func recordHistory(chatID int64, userText, reply string) {
	// defer Unlock so a panic inside this section can't leave histMu locked forever (which
	// would freeze every chat, the history saver, and graceful shutdown).
	histMu.Lock()
	defer histMu.Unlock()
	if history == nil { // guard against a nil map (e.g. loaded from a `null` history file)
		history = map[int64][]chatMsg{}
	}
	h := append(history[chatID], chatMsg{Role: "user", Content: userText}, chatMsg{Role: "assistant", Content: reply})
	if len(h) > maxHistory {
		h = h[len(h)-maxHistory:]
	}
	history[chatID] = h
	markHistoryDirty()
	// Persist the exchange to long-term memory (async inside; no-op if memory is off).
	rememberExchange(chatID, userText, reply)
}

func parseAllow(raw string) map[int64]bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil // no allowlist → open to everyone
	}
	set := map[int64]bool{}
	for _, p := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' }) {
		if id, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64); err == nil {
			set[id] = true
		}
	}
	return set
}

// ==================== Telegram plumbing ====================

type tgPhotoSize struct {
	FileID string `json:"file_id"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

type tgDocument struct {
	FileID   string `json:"file_id"`
	MimeType string `json:"mime_type"`
	FileName string `json:"file_name"`
}

type tgVoice struct {
	FileID   string `json:"file_id"`
	Duration int    `json:"duration"`
	MimeType string `json:"mime_type"`
}

type tgMessage struct {
	Text    string        `json:"text"`
	Caption string        `json:"caption"`
	Photo   []tgPhotoSize `json:"photo"`
	Chat    struct {
		ID int64 `json:"id"`
	} `json:"chat"`
	Document *tgDocument `json:"document"`
	Voice    *tgVoice    `json:"voice"`
	Audio    *tgVoice    `json:"audio"`
}

type tgUpdate struct {
	UpdateID int        `json:"update_id"`
	Message  *tgMessage `json:"message"`
}

// firstAudio returns the message's voice note or audio file, if any.
func firstAudio(m *tgMessage) *tgVoice {
	if m.Voice != nil {
		return m.Voice
	}
	return m.Audio
}

// imageFileID returns the Telegram file_id of an image in the message (largest photo
// size, or an image document), plus true if the message carries an image.
func imageFileID(m *tgMessage) (string, bool) {
	if len(m.Photo) > 0 {
		best := m.Photo[0]
		for _, p := range m.Photo {
			if p.Width > best.Width {
				best = p
			}
		}
		return best.FileID, true
	}
	if m.Document != nil && strings.HasPrefix(m.Document.MimeType, "image/") {
		return m.Document.FileID, true
	}
	return "", false
}

// pollHTTP must outlive the server-side long-poll window (timeout=20), or a silently
// dropped connection would block getUpdates forever and freeze the whole bot.
var pollHTTP = &http.Client{Timeout: 35 * time.Second}

// tgHTTP is the shared client for all short Telegram calls (sendMessage/editMessageText/
// sendChatAction). It MUST have a timeout: http.DefaultClient has none, so a half-open TCP
// connection would hang a call forever — and sendTelegramMessage runs on the poller
// goroutine, so one frozen send would silently kill the whole bot.
var tgHTTP = &http.Client{Timeout: 30 * time.Second}

// tgUploadHTTP is for multipart file uploads (sendPhoto/sendVideo/sendDocument, up to ~49 MB),
// which need a longer ceiling than plain messages.
var tgUploadHTTP = &http.Client{Timeout: 180 * time.Second}

// secretToken holds the bot token so redact() can scrub it from strings; set once in main().
var secretToken string

// redact removes the bot token from any string before it reaches a chat or a log file.
// Telegram's *url.Error.Error() prints the full request URL, which embeds the token
// (https://api.telegram.org/bot<TOKEN>/... and .../file/bot<TOKEN>/...) — a network error
// during a getFile/download would otherwise leak the token to whoever is chatting.
func redact(s string) string {
	if secretToken != "" {
		s = strings.ReplaceAll(s, secretToken, "***")
	}
	return s
}

// redactErr is redact(err.Error()) with a nil guard, for logging Telegram/file errors.
func redactErr(err error) string {
	if err == nil {
		return ""
	}
	return redact(err.Error())
}

func getTelegramUpdates(botAPI string, offset int) ([]tgUpdate, error) {
	u := fmt.Sprintf("%s/getUpdates?offset=%d&timeout=20", botAPI, offset)
	resp, err := pollHTTP.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var data struct {
		Ok          bool       `json:"ok"`
		ErrorCode   int        `json:"error_code"`
		Description string     `json:"description"`
		Result      []tgUpdate `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	if !data.Ok {
		// 409 = another poller holds getUpdates for this token. Back off so the
		// two instances do not hammer the API; the survivor keeps working.
		return nil, fmt.Errorf("telegram api %d: %s", data.ErrorCode, data.Description)
	}
	return data.Result, nil
}

// sendTelegramMessage formats the reply as Telegram HTML (bold/italic/code), splitting
// long messages, and falls back to plain text if the HTML send fails.
func sendTelegramMessage(botAPI string, chatID int64, text string) {
	text = redact(stripThink(text)) // last-resort scrub so a leaked token never reaches a chat
	for _, chunk := range splitMessage(text, 3000) {
		sendTelegramChunk(botAPI, chatID, chunk)
	}
}

// sendTelegramChunk delivers one already-split chunk: HTML first, plain-text fallback.
func sendTelegramChunk(botAPI string, chatID int64, chunk string) {
	html := toTelegramHTML(chunk)
	if sendTelegramRaw(botAPI, chatID, html, "HTML") {
		return
	}
	if !sendTelegramRaw(botAPI, chatID, stripTags(html), "") {
		log.Printf("[TELEGRAM] send failed to %d (html and plain)", chatID)
	}
}

// sendChatAction shows a status (e.g. "typing") under the bot name.
func sendChatAction(botAPI string, chatID int64, action string) {
	form := url.Values{}
	form.Set("chat_id", strconv.FormatInt(chatID, 10))
	form.Set("action", action)
	if resp, err := tgHTTP.PostForm(botAPI+"/sendChatAction", form); err == nil {
		resp.Body.Close()
	}
}

// startTyping shows "печатает…" and keeps it alive (Telegram clears it after ~5s)
// until the returned stop() is called — a live indicator that work is in progress.
func startTyping(botAPI string, chatID int64) func() {
	done := make(chan struct{})
	go func() {
		sendChatAction(botAPI, chatID, "typing")
		t := time.NewTicker(4 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				sendChatAction(botAPI, chatID, "typing")
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}

func sendTelegramRaw(botAPI string, chatID int64, text, parseMode string) bool {
	form := url.Values{}
	form.Set("chat_id", strconv.FormatInt(chatID, 10))
	form.Set("text", text)
	if parseMode != "" {
		form.Set("parse_mode", parseMode)
	}
	resp, err := tgHTTP.PostForm(botAPI+"/sendMessage", form)
	if err != nil {
		log.Printf("[TELEGRAM] send error to %d: %s", chatID, redactErr(err))
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200
}

// splitMessage breaks text into ≤limit-byte chunks, preferring line boundaries and never
// tearing a fenced ``` code block across chunks: an open block is closed at the end of one
// chunk and re-opened (same language) at the start of the next, so each chunk renders as
// valid Markdown on its own. A single line longer than the limit is hard-split on a UTF-8
// rune boundary (Russian is 2 bytes/char — a mid-rune cut is invalid UTF-8 Telegram rejects).
func splitMessage(text string, limit int) []string {
	if len(text) <= limit {
		return []string{text}
	}
	var out []string
	var cur strings.Builder
	inFence := false
	fenceOpen := "" // the opening fence line (e.g. "```python") to re-open with

	// appendLine joins lines with a single "\n" between them (never a trailing one), so
	// reassembling the chunks reproduces the input without extra newlines.
	appendLine := func(line string) {
		if cur.Len() > 0 {
			cur.WriteString("\n")
		}
		cur.WriteString(line)
	}
	flush := func(reopen bool) {
		s := cur.String()
		if inFence {
			s += "\n```" // close the block this chunk left open
		}
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
		cur.Reset()
		if reopen && inFence {
			cur.WriteString(fenceOpen)
		}
	}

	for _, line := range strings.Split(text, "\n") {
		projected := cur.Len() + len(line) + 1
		if inFence {
			projected += 4 // room for the closing "\n```"
		}
		if projected > limit && cur.Len() > 0 {
			flush(true)
		}
		if len(line) > limit { // a single over-long line: hard-split it
			if cur.Len() > 0 {
				flush(true)
			}
			for len(line) > limit {
				cut := limit
				for cut > 0 && !utf8.RuneStart(line[cut]) {
					cut--
				}
				if cut == 0 {
					cut = limit
				}
				out = append(out, line[:cut])
				line = line[cut:]
			}
		}
		appendLine(line)
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			if inFence {
				inFence, fenceOpen = false, ""
			} else {
				inFence, fenceOpen = true, line
			}
		}
	}
	flush(false)
	return out
}
