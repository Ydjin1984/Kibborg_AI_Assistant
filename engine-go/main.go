package main

// Kibborg — minimal Telegram ↔ LLM bridge.
// Two jobs only: (1) launch the local LLM (llama-server) and (2) let the user chat
// with it through Telegram. All other tooling was intentionally removed.

import (
	"context"
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

Примечание: в обычном режиме (без tool-agent) у тебя нет живого интернета. Если пользователь в allowlist — движок сам переключает тебя на tool-agent с терминалом и поиском; не утверждай обратное вне того режима.

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

// chartVerdictRules is the trade-filter discipline for /chart. It used to be a vision system
// prompt that read prices off the screenshot — which is exactly the failure §7 forbids: vision
// misreads «BTC 118 024» by a digit and the whole plan runs on an invented number.
//
// So it moved here, into the TASK text of the /chart agent run: the same "reject weak setups"
// filter and the same output template, but every number comes from analyze_ticker (Binance).
const chartVerdictRules = `
Ты не аналитик, который ищет сделку любой ценой, а ТОРГОВЫЙ ФИЛЬТР: отбраковывай плохие входы.
Выбирай WAIT, если верно хотя бы одно: вероятность ниже 70%; стоп дальше 5% от входа;
R:R хуже 1:2; структура неоднозначна; ключевые уровни неочевидны.

ВСЕ ЦЕНЫ бери из результата analyze_ticker (данные Binance). Ни одной цифры со скриншота.

Формат ответа — строго по шаблону, без вступлений:

⚪ WAIT
Причины:
- <пункт>
- <пункт>

либо (🟢 LONG / 🔴 SHORT):

🟢 LONG
Направление: LONG
Вероятность: NN%
Вход / DCA1 / DCA2 / SL / TP1 / TP2 / TP3: <цены из analyze_ticker>
R:R: 1:N
Причины ЗА: - <пункт>
Причины ПРОТИВ: - <пункт>
Финальный вердикт: LONG

Лучше честный WAIT, чем выдуманная сделка.`

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
	// Agent safety state: the hands switch (runtime store, never settings.ini §6.4), the
	// write/delete allowlist roots (§6.1), and any confirmation that died with the previous
	// process — the honest answer to those is «задача потерялась», not a blind replay (§6.3).
	loadHandsMode()
	setHandsRoots(cfg.HandsRoots)
	reportStalePending()
	go expirePendingLoop(func(chatID int64, channel, text string) {
		if channel == channelTelegram && cfg.TelegramToken != "" {
			sendTelegramMessage("https://api.telegram.org/bot"+cfg.TelegramToken, chatID, text)
		}
	})
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
	// Кнопка «☰» со списком команд — регистрируется у Telegram один раз при старте.
	go installTelegramMenu(botAPI)

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
			// Нажатие кнопки панели. Обрабатывается ЗДЕСЬ, а не через очередь чата: половина
			// кнопок («Стоп», переключение рук) обязана срабатывать посреди идущей задачи, а
			// очередь по определению ждёт её конца.
			if cb := u.CallbackQuery; cb != nil {
				go handleCallbackQuery(cfg, botAPI, allow, cb)
				continue
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
			// /stop and /hands are handled HERE, before the queue (§4.2, §9). The per-chat
			// worker is strictly serial: while a task runs, anything enqueued executes AFTER
			// it — so a queued /stop would arrive too late to stop anything.
			if handledPreQueue(cfg, botAPI, allow, msg) {
				continue
			}
			enqueueMessage(cfg, botAPI, allow, msg)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// stopCommands / handsCommands are the two controls that must work WHILE a task is running.
var (
	stopCommands  = []string{"/stop", "/стоп", "/отмена", "/cancel"}
	handsCommands = []string{"/hands", "/руки"}
)

// handledPreQueue processes the controls that cannot wait in the per-chat queue and reports
// whether the message is fully handled.
//
// It runs OUTSIDE the normal handler, which is exactly why the permission check is repeated
// here: forget it and any chat could stop the owner's tasks or flip the hands switch (§4.2).
func handledPreQueue(cfg Config, botAPI string, allow map[int64]bool, msg *tgMessage) bool {
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return false
	}
	chatID := msg.Chat.ID
	isStop, _ := parseCommand(text, stopCommands)
	isHands, handsArg := parseCommand(text, handsCommands)
	isMenu, _ := parseCommand(text, menuCommands)
	if !isStop && !isHands && !isMenu {
		return false
	}
	if !isOwnerChat(allow, chatID) {
		// A foreign chat's /stop must not touch anyone's task (приёмка №24).
		log.Printf("[TELEGRAM] pre-queue %q из чата %d вне allowlist — игнор", text, chatID)
		sendTelegramMessage(botAPI, chatID, "⛔ Эта команда доступна только владельцу (TELEGRAM_ID).")
		return true
	}

	// /menu — вне очереди: панель должна открываться и показывать состояние ИМЕННО тогда,
	// когда что-то идёт не так, то есть посреди работающей задачи.
	if isMenu {
		sendTelegramMenu(botAPI, cfg, chatID)
		return true
	}

	if isStop {
		if taskID, ok := stopActiveTask(chatID); ok {
			log.Printf("[TELEGRAM] /stop → задача %s остановлена", taskID)
			sendTelegramMessage(botAPI, chatID, "⏹ Остановлено.")
		} else {
			sendTelegramMessage(botAPI, chatID, "Нечего останавливать — активной задачи нет.")
		}
		return true
	}

	// /hands — the runtime switch (§6.4); never settings.ini.
	arg := strings.TrimSpace(handsArg)
	if arg == "" {
		// Без аргумента показываем панель: переключить кнопкой быстрее, чем вспоминать слово.
		sendTelegramWithMarkup(botAPI, chatID, handsModeLabel(currentHandsMode()), menuKeyboard())
		return true
	}
	mode := setHandsMode(arg, fmt.Sprintf("telegram:%d", chatID))
	sendTelegramWithMarkup(botAPI, chatID, handsModeLabel(mode), menuKeyboard())
	return true
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

	// --- Media auto-route (image → vision, voice → STT, file → text, video → hint) ---
	// Plain text never attaches image_url, so vision is not used on text turns.

	// Image (screenshot/photo/image-file) → vision chat only.
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
		stop := startTyping(botAPI, chatID)
		defer stop() // safety net; explicit stop() below clears it before the reply is sent
		if chartReq {
			// §7: the screenshot goes through vision WITHOUT tools, and the numbers for the
			// analysis come from Binance — never from what vision "read" on the chart.
			desc, err := describeImage(cfg, chatID, chartVisionPrompt, fileID, chartVisionSystemPrompt)
			if err != nil {
				stop()
				sendTelegramMessage(botAPI, chatID, "❌ "+err.Error())
				return
			}
			stop()
			handleChartAnalysis(cfg, botAPI, allow, chatID, extra, desc)
			return
		}
		if prompt == "" {
			// A bare picture: the description IS the answer, no routing needed.
			reply := chatWithImage(cfg, chatID, "Что на этом изображении? Опиши по делу.", fileID, "")
			stop()
			sendTelegramMessage(botAPI, chatID, reply)
			log.Printf("[TELEGRAM] replied to %d (image, %d chars)", chatID, len(reply))
			return
		}
		// Caption + picture: describe first, then let the dispatcher route the TEXT (§4).
		desc, err := describeImage(cfg, chatID, "Опиши это изображение по делу, без выдумок: что на нём и какие детали важны.", fileID, chartVisionSystemPrompt)
		if err != nil {
			stop()
			sendTelegramMessage(botAPI, chatID, "❌ "+err.Error())
			return
		}
		stop()
		if allow == nil {
			sendTelegramMessage(botAPI, chatID, desc)
			recordHistory(chatID, "[изображение] "+prompt, desc)
			return
		}
		runTelegramAgent(cfg, botAPI, allow, chatID,
			prompt+"\n\n[Описание приложенной картинки, полученное зрением]\n"+desc)
		return
	}

	// Voice / audio → Whisper transcription, then the normal text chat pipeline (no vision).
	if v := firstAudio(msg); v != nil {
		handleVoiceMessage(cfg, botAPI, chatID, v.FileID, v.Duration, v.MimeType)
		return
	}

	// Видео → ffmpeg разбирает его на речь и кадры, дальше это обычный ТЕКСТОВЫЙ запрос (§21).
	if v := firstVideo(msg); v != nil {
		handleTelegramVideo(cfg, botAPI, allow, chatID, v, strings.TrimSpace(msg.Caption))
		return
	}

	// Document: classify by name+mime — image / audio / video / text / other.
	if msg.Document != nil {
		// Security tools work on ANY file (incl. binaries): /scan, /audit, /logs in the caption.
		if mode, ok := parseSecFileCaption(msg.Caption); ok {
			handleSecDocument(cfg, botAPI, chatID, msg.Document, mode)
			return
		}
		kind := classifyMedia(msg.Document.FileName, msg.Document.MimeType)
		switch kind {
		case mediaImage:
			// Image sent as a file (not compress photo) — same vision path.
			if !brainReady(cfg.BrainPort) {
				sendTelegramMessage(botAPI, chatID, "⏳ Модель ещё грузится. Пришли картинку чуть позже.")
				return
			}
			prompt := strings.TrimSpace(msg.Caption)
			if prompt == "" {
				prompt = "Что на этом изображении? Опиши по делу."
			}
			stop := startTyping(botAPI, chatID)
			reply := chatWithImage(cfg, chatID, prompt, msg.Document.FileID, "")
			stop()
			sendTelegramMessage(botAPI, chatID, reply)
			log.Printf("[TELEGRAM] replied to %d (image-doc %s, %d chars)", chatID, msg.Document.FileName, len(reply))
			return
		case mediaAudio:
			handleVoiceMessage(cfg, botAPI, chatID, msg.Document.FileID, 0, msg.Document.MimeType)
			return
		case mediaPDF:
			// PDF → текстовый слой или распознавание скана → дальше обычный текст (§23).
			handleTelegramPDF(cfg, botAPI, allow, chatID, msg.Document, strings.TrimSpace(msg.Caption))
			return
		case mediaVideo:
			// Видео файлом — тот же разбор, что и видео сообщением.
			handleTelegramVideo(cfg, botAPI, allow, chatID, &tgVideo{
				FileID:   msg.Document.FileID,
				MimeType: msg.Document.MimeType,
				FileName: msg.Document.FileName,
				FileSize: msg.Document.FileSize,
			}, strings.TrimSpace(msg.Caption))
			return
		case mediaTextFile:
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
		default:
			sendTelegramMessage(botAPI, chatID,
				"❌ Этот формат пока не читаю.\n"+
					"• Картинки → зрение (png/jpg/webp…)\n"+
					"• Голос → STT\n"+
					"• Текст/код → .go .py .md .txt .json …\n"+
					"• Видео по ссылке → /download")
			return
		}
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
	isCompact, _ := parseCommand(text, compactCommands)
	isHW, _ := parseCommand(text, hardwareCommands)
	isModels, modelsArg := parseCommand(text, modelsCommands)
	switch {
	case strings.HasPrefix(lower, "/start"), strings.HasPrefix(lower, "/help"):
		sendTelegramWithMarkup(botAPI, chatID,
			"Kibborg на связи. Просто пиши сообщение (или голосовое) — я отвечаю через локальную LLM.\n"+
				"🎛 /menu — панель: руки, стоп, сжатие контекста, статус. Список команд — кнопка «☰» слева от поля ввода.\n"+
				"🖥 Рабочий стол: «сделай скриншот экрана», «что у меня открыто», «открой блокнот и напиши…» — я вижу экран и управляю мышью и клавиатурой.\n"+
				"🗜 /compact — сжать историю диалога в сводку, не теряя сути.\n"+
				"🎙 Голос: TypeWhisper (HTTP API) → текст → ответ; fallback whisper.cpp. В Web — кнопка 🎙.\n"+
				"🎬 Видео: пришли ролик (или ссылку) — распознаю речь, посмотрю кадры и отвечу по содержанию. "+
				"С подписью «найди этот проект на гитхабе» — сразу и найду. Файл на диске: «разбери D:\\видео\\урок.mp4».\n"+
				"🖥 /hw — тест железа: сокеты, ядра, потоки, RAM, карты, VRAM.\n"+
				"📦 /models [запрос] — каталог GGUF Hugging Face под твоё железо. Скачать: /models get owner/repo file.gguf\n"+
				"📊 /analyze <тикер> — детерминированный разбор по данным Binance (режим, скор, тренды). Пример: /analyze BTC\n"+
				"📈 /chart — торговый разбор графика: отправь команду, затем пришли скриншот или файл графика.\n"+
				"📐 /size — размер позиции по риску. Пример: /size BTC entry=50000 stop=49000 tp=53000 risk=1.5 lev=10\n"+
				"📝 /log — записать сделку в журнал · /journal — статистика и список · /close <id> <цена> — закрыть сделку\n"+
				"🛡 /logs — анализ логов (по умолчанию свои: аномалии, всплески ошибок, утечки секретов; журналы агента — `/logs runtime/hands.jsonl` и `/logs runtime/tasks.jsonl`) · /scan <текст> — поиск IOC и сигнатур атак · /audit — хеш+энтропия файла (пришли документом с подписью)\n"+
				"🌐 /browser <задача> или /agent <задача> — полный агент: терминал, файлы, поиск в интернете, Chrome, Agent Reach.\n"+
				"   В обычном чате тоже: «найди…», «запусти…», «прочитай файл…» — сам выберет инструменты (нужен TELEGRAM_ID).\n"+
				"⏹ /stop — остановить текущую задачу (работает сразу, даже посреди команды).\n"+
				"🖐 /hands — длина рук: `safe` (короткие: опасное спрашивает) ↔ `full` (длинные: весь ПК без вопросов). "+
				"Недостижимого нет: даже ядерные команды в длинных руках не запрещены, а переспрашиваются один раз.\n"+
				"   На опасный шаг отвечай **да** / **нет** — подтверждение не блокирует бота.\n"+
				"🎬 /download <url> — скачать видео (YouTube, Instagram, TikTok…) в макс. качестве. Можно просто прислать ссылку.\n"+
				"🧠 Я помню прошлые диалоги (долговременная память) и подмешиваю нужное в ответ.\n"+
				"/reset — очистить контекст диалога и долговременную память.",
			menuKeyboard())
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
		// Full agent: terminal + files + web + Chrome. Remote control of the user's machine —
		// require TELEGRAM_ID allowlist.
		if allow == nil {
			sendTelegramMessage(botAPI, chatID,
				"🔒 /browser и /agent отключены, пока не задан TELEGRAM_ID в settings.ini: агент управляет терминалом и Chrome на твоём ПК. Впиши свой chat id и перезапусти бота.")
			return
		}
		if !brainReady(cfg.BrainPort) {
			sendTelegramMessage(botAPI, chatID, "⏳ Модель ещё грузится. Повтори запрос чуть позже.")
			return
		}
		if browserTask == "" {
			sendTelegramMessage(botAPI, chatID,
				"🌐 Напиши задачу: /agent <что сделать> (или /browser).\n"+
					"Примеры:\n"+
					"• /agent найди свежие статьи про Rust async\n"+
					"• /agent покажи файлы в D:\\projects\n"+
					"• /agent собери таблицу с открытой вкладки Chrome\n"+
					"Терминал, файлы, интернет и Chrome — в одном агенте.")
			return
		}
		// A slash command is a HINT to the dispatcher, not a bypass of it (§7).
		runTelegramAgent(cfg, botAPI, allow, chatID, browserTask)
		return
	case isHW:
		sendTelegramMessage(botAPI, chatID, formatHardwareText(probeHardware(true)))
		return
	case isModels:
		sendTelegramMessage(botAPI, chatID, handleModelsCommand(modelsArg))
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
		resetChatContext(chatID)
		sendTelegramMessage(botAPI, chatID, "♻️ Контекст диалога и долговременная память очищены.")
		return
	case isCompact:
		// Сжатие — это вызов модели, поэтому команда идёт через очередь чата, а не мимо неё.
		stop := startTyping(botAPI, chatID)
		reply := handleCompactCommand(cfg, chatID)
		stop()
		sendTelegramMessage(botAPI, chatID, reply)
		return
	}

	if !brainReady(cfg.BrainPort) {
		sendTelegramMessage(botAPI, chatID,
			"⏳ Модель ещё грузится в память (1-5 минут после старта). Попробуй чуть позже.")
		return
	}

	// A reply to a pending confirmation goes first: "да"/"нет" resumes the parked task
	// instead of starting a new one (§6.3 п. 8). The worker is free by now — the paused task
	// released browserTaskMu — so this path can safely go through the queue.
	if p := peekPending(chatID); p != nil {
		if yes, ok := confirmWord(text); ok {
			if !isOwnerChat(allow, chatID) {
				sendTelegramMessage(botAPI, chatID, "⛔ Подтверждать действия может только владелец.")
				return
			}
			answerTelegramConfirmation(cfg, botAPI, chatID, yes)
			return
		}
	}

	// Allowlisted chats: ALWAYS the layered agent (dispatcher → packs → guarded tools).
	// Plain streaming has no tools — that was why the model invented news and claimed
	// "no console". Without TELEGRAM_ID — streaming only (no remote shell for strangers).
	if allow != nil && wantsToolAgent(text) {
		runTelegramAgent(cfg, botAPI, allow, chatID, text)
		return
	}

	// Open / non-allowlisted: stream without tools.
	stop := startTyping(botAPI, chatID)
	defer stop()
	chatWithHistoryStream(cfg, botAPI, chatID, text, stop)
	stop()
}

// runTelegramAgent is the single Telegram entry into the layered agent: statuses go out as
// they happen, the final answer carries the dispatcher's summary, and only a COMPLETED task
// is written to long-term memory (§4.1 — interrupted attempts must not surface in recall).
func runTelegramAgent(cfg Config, botAPI string, allow map[int64]bool, chatID int64, text string) {
	stop := startTyping(botAPI, chatID)
	defer stop()

	actor := actorFor(channelTelegram, chatID, isOwnerChat(allow, chatID))
	task := readFollowUpHint(chatID, text)
	base := withMemory(cfg, chatID, text, baseMessages(chatID))

	res := runAgent(cfg, actor, base, task, telegramStatus(botAPI, chatID))
	stop()
	deliverAgentResult(botAPI, chatID, text, res)
	// Окно диалога подошло к пределу — пересказываем старое в сводку вместо того, чтобы молча
	// его выбросить. В фоне: пользователь уже получил ответ и ждать сжатия не должен.
	maybeAutoCompact(cfg, chatID, func(note string) { sendTelegramMessage(botAPI, chatID, note) })
}

// handleChartAnalysis is the /chart tail: a vision DESCRIPTION plus a ticker, analysed by the
// trade pack over live Binance candles (§7, приёмка №33). If the ticker cannot be resolved we
// ask — a wrong guess here poisons every number downstream.
func handleChartAnalysis(cfg Config, botAPI string, allow map[int64]bool, chatID int64, extra, desc string) {
	symbol := extractChartTicker(extra, desc)
	if symbol == "" {
		sendTelegramMessage(botAPI, chatID,
			"📈 Разобрал картинку, но тикер по ней не определился:\n\n"+capAgentText(desc, 1200)+
				"\n\n❓ Назови инструмент (например `BTC` или `ETHUSDT`) — возьму данные с биржи и сделаю разбор. "+
				"Цифры со скриншота я как данные не использую.")
		setChartPending(chatID, strings.TrimSpace(extra+"\n"+desc))
		return
	}
	if allow == nil {
		// No allowlist → no agent. Still give the deterministic report, it needs no hands.
		report, err := analyzeTicker(symbol)
		if err != nil {
			sendTelegramMessage(botAPI, chatID, "❌ "+err.Error())
			return
		}
		sendTelegramMessage(botAPI, chatID, renderReport(report)+sizingBlock(cfg, report))
		return
	}
	task := chartTaskText(symbol, extra, desc)
	stop := startTyping(botAPI, chatID)
	defer stop()
	actor := actorFor(channelTelegram, chatID, isOwnerChat(allow, chatID))
	base := withMemory(cfg, chatID, task, baseMessages(chatID))
	res := runLayeredAgent(agentRequest{
		cfg: cfg, actor: actor, baseMsgs: base, input: task,
		status:     telegramStatus(botAPI, chatID),
		memSummary: memorySummaryFor(chatID),
		hintPacks:  []string{packTrade},
	})
	stop()
	deliverAgentResult(botAPI, chatID, "[график] "+symbol, res)
}

// chartTaskText builds the /chart task: analyse the resolved ticker with live data, using the
// screenshot only as STRUCTURE, and answer through the trade filter (§7).
func chartTaskText(symbol, extra, desc string) string {
	task := "Разбери " + symbol + ": вызови analyze_ticker и дай торговый вывод.\n" +
		chartVerdictRules +
		"\n\n[Описание графика со скриншота — только структура, числа отсюда НЕ бери]\n" +
		capAgentText(desc, 1200)
	if strings.TrimSpace(extra) != "" {
		task += "\n\nКонтекст от пользователя: " + strings.TrimSpace(extra)
	}
	return task
}

// telegramStatus throttles step statuses so a long task does not spam the chat.
func telegramStatus(botAPI string, chatID int64) func(string) {
	var last string
	var lastAt time.Time
	return func(s string) {
		if s == "" || s == last || time.Since(lastAt) < 2*time.Second {
			return
		}
		last, lastAt = s, time.Now()
		sendTelegramMessage(botAPI, chatID, s)
	}
}

// deliverAgentResult sends notices, the answer and the artifacts, then records history.
func deliverAgentResult(botAPI string, chatID int64, userText string, res agentResult) {
	for _, n := range res.Notices {
		sendTelegramMessage(botAPI, chatID, n)
	}
	if strings.TrimSpace(res.Text) != "" {
		sendTelegramMessage(botAPI, chatID, res.Text)
	}
	sendArtifacts(botAPI, chatID, res.Artifacts)
	if !res.interrupted() {
		recordHistory(chatID, userText, res.Text)
	}
	log.Printf("[AGENT] %s → %d (%s, %d chars, %d artifacts)",
		res.TaskID, chatID, res.Status, len(res.Text), len(res.Artifacts))
}

// answerTelegramConfirmation resumes (or refuses) the parked task.
func answerTelegramConfirmation(cfg Config, botAPI string, chatID int64, approved bool) {
	rs := takePending(chatID)
	if rs == nil {
		sendTelegramMessage(botAPI, chatID, "Нечего подтверждать — вопрос уже снят.")
		return
	}
	stop := startTyping(botAPI, chatID)
	defer stop()
	res := resumeConfirmed(rs, approved, telegramStatus(botAPI, chatID))
	stop()
	deliverAgentResult(botAPI, chatID, rs.task.Input, res)
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

// browserCommands open the full tool Agent (terminal + web + Chrome). Aliases kept for habit.
var browserCommands = []string{"/browser", "/браузер", "/web", "/agent", "/агент", "/tools"}

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
	// Direct /download bypasses the agent loop, so give it its own task-shaped budget: the
	// same TaskTimeout ceiling applies, and yt-dlp dies with it (§4.2).
	ctx, cancel := context.WithTimeout(context.Background(), TaskTimeout)
	defer cancel()
	res, err := browser.FetchVideo(ctx, url, cfg.FfmpegPath)
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
	FileSize int64  `json:"file_size"`
}

type tgVoice struct {
	FileID   string `json:"file_id"`
	Duration int    `json:"duration"`
	MimeType string `json:"mime_type"`
}

// tgVideo покрывает video, video_note (кружок) и animation (гифка) — для разбора это один
// и тот же контейнер, разница только в том, как Telegram его показывает.
type tgVideo struct {
	FileID   string `json:"file_id"`
	Duration int    `json:"duration"`
	MimeType string `json:"mime_type"`
	FileName string `json:"file_name"`
	FileSize int64  `json:"file_size"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
}

type tgMessage struct {
	MessageID int           `json:"message_id"`
	Text      string        `json:"text"`
	Caption   string        `json:"caption"`
	Photo     []tgPhotoSize `json:"photo"`
	Chat      struct {
		ID int64 `json:"id"`
	} `json:"chat"`
	Document  *tgDocument `json:"document"`
	Voice     *tgVoice    `json:"voice"`
	Audio     *tgVoice    `json:"audio"`
	Video     *tgVideo    `json:"video"`
	VideoNote *tgVideo    `json:"video_note"`
	Animation *tgVideo    `json:"animation"`
}

// tgCallbackQuery is a press on an inline-keyboard button (the /menu panel).
type tgCallbackQuery struct {
	ID   string `json:"id"`
	Data string `json:"data"`
	From struct {
		ID int64 `json:"id"`
	} `json:"from"`
	Message *tgMessage `json:"message"`
}

type tgUpdate struct {
	UpdateID      int              `json:"update_id"`
	Message       *tgMessage       `json:"message"`
	CallbackQuery *tgCallbackQuery `json:"callback_query"`
}

// firstAudio returns the message's voice note or audio file, if any.
func firstAudio(m *tgMessage) *tgVoice {
	if m.Voice != nil {
		return m.Voice
	}
	return m.Audio
}

// firstVideo returns the message's video, video note (circle) or animation, if any.
func firstVideo(m *tgMessage) *tgVideo {
	switch {
	case m.Video != nil:
		return m.Video
	case m.VideoNote != nil:
		return m.VideoNote
	case m.Animation != nil:
		return m.Animation
	}
	return nil
}

// imageFileID returns the Telegram file_id of an image in the message (largest photo
// size, or an image document by mime/extension), plus true if the message carries an image.
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
	// Image-as-file: mime image/* OR extension .png/.jpg/… (Telegram often mangles mime).
	if m.Document != nil && classifyMedia(m.Document.FileName, m.Document.MimeType) == mediaImage {
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
