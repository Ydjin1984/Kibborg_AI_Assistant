package main

// Меню команд в Telegram: список у кнопки «☰» и живая инлайн-панель по /menu.
//
// Зачем: рубильник рук и /stop существовали только как текстовые команды, которые надо было
// помнить и печатать. Кнопка убирает и то, и другое — и заодно показывает ТЕКУЩЕЕ состояние
// (какие руки включены прямо сейчас), а не только позволяет его сменить.
//
// Нажатия обрабатываются ВНЕ очереди чата, там же, где /stop и /hands (§4.2): очередь строго
// последовательна, и кнопка «Стоп», поставленная в очередь за длинной задачей, останавливала
// бы уже следующую.

import (
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

// menuCommands открывают инлайн-панель.
var menuCommands = []string{"/menu", "/меню", "/кнопки", "/panel"}

// tgBotCommand is one row of the "/" list Telegram shows above the input field.
type tgBotCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// botCommandList — то, что видит пользователь, нажав «☰» или набрав «/».
// Порядок важен: сверху то, чем пользуются каждый день.
func botCommandList() []tgBotCommand {
	return []tgBotCommand{
		{"menu", "🎛 Панель управления: руки, контекст, стоп"},
		{"hands", "🖐 Короткие / длинные руки (доступ к ПК)"},
		{"stop", "⏹ Остановить текущую задачу"},
		{"compact", "🗜 Сжать контекст диалога в сводку"},
		{"reset", "♻️ Очистить контекст и долговременную память"},
		{"agent", "🌐 Задача агенту: терминал, файлы, интернет, Chrome, рабочий стол"},
		{"analyze", "📊 Разбор тикера по данным Binance"},
		{"tts", "🔊 Озвучка: всегда / по запросу"},
		{"speak", "🔈 Озвучить последний ответ"},
		{"hw", "💻 Тест железа: CPU, RAM, GPU, VRAM"},
		{"models", "📦 Модели: статус, скачать, переключить"},
		{"chart", "📈 Торговый разбор скриншота графика"},
		{"size", "📐 Размер позиции по риску"},
		{"log", "📝 Записать сделку в журнал"},
		{"journal", "📒 Статистика журнала сделок"},
		{"close", "✅ Закрыть сделку по id"},
		{"download", "🎬 Скачать видео по ссылке"},
		{"logs", "🛡 Анализ логов"},
		{"scan", "🔎 Поиск IOC в тексте"},
		{"audit", "🧬 Хеш и энтропия файла"},
		{"stress", "🛡 Тест на прочность (light/required/full) → MD"},
		{"help", "❓ Что я умею"},
	}
}

// installTelegramMenu registers the command list and turns the ☰ button into a command menu.
// Ошибки не фатальны: бот работает и без меню, просто команды придётся печатать руками.
func installTelegramMenu(botAPI string) {
	body, err := json.Marshal(map[string]any{"commands": botCommandList()})
	if err != nil {
		return
	}
	if err := telegramPostJSON(botAPI, "setMyCommands", body); err != nil {
		log.Printf("[TELEGRAM] setMyCommands: %v", err)
		return
	}
	menuBody, _ := json.Marshal(map[string]any{"menu_button": map[string]any{"type": "commands"}})
	if err := telegramPostJSON(botAPI, "setChatMenuButton", menuBody); err != nil {
		log.Printf("[TELEGRAM] setChatMenuButton: %v", err)
	}
	log.Printf("[TELEGRAM] меню команд установлено (%d команд)", len(botCommandList()))
}

// ===== инлайн-панель =====

// Данные кнопок. Короткие строки: Telegram ограничивает callback_data 64 байтами.
const (
	cbHandsFull = "h:full"
	cbHandsSafe = "h:safe"
	cbCompact   = "x:compact"
	cbReset     = "x:reset"
	cbStop      = "x:stop"
	cbStatus    = "x:status"
	cbSkills    = "x:skills"
	cbRefresh   = "x:menu"
	cbTTSAuto   = "t:auto"
	cbTTSAsk    = "t:ask"
	cbSpeak     = "t:say"
)

// menuKeyboard renders the inline panel for the CURRENT state: активный режим рук помечен
// галочкой, чтобы кнопка была индикатором, а не только переключателем.
func menuKeyboard() string {
	mode := currentHandsMode()
	tm := currentTTSMode()
	mark := func(on bool, label string) string {
		if on {
			return "✅ " + label
		}
		return label
	}
	kb := map[string]any{
		"inline_keyboard": [][]map[string]string{
			{
				{"text": mark(mode == handsModeSafe, "🛡 Короткие руки"), "callback_data": cbHandsSafe},
				{"text": mark(mode == handsModeFull, "🖐 Длинные руки"), "callback_data": cbHandsFull},
			},
			{
				{"text": mark(tm == ttsModeAsk, "🔇 Озвучка по запросу"), "callback_data": cbTTSAsk},
				{"text": mark(tm == ttsModeAuto, "🔊 Всегда озвучивать"), "callback_data": cbTTSAuto},
			},
			{
				{"text": "🔈 Озвучить ответ", "callback_data": cbSpeak},
			},
			{
				{"text": "🗜 Сжать контекст", "callback_data": cbCompact},
				{"text": "♻️ Сброс контекста", "callback_data": cbReset},
			},
			{
				{"text": "⏹ Стоп", "callback_data": cbStop},
				{"text": "🩺 Статус", "callback_data": cbStatus},
			},
			{
				{"text": "🧰 Что я умею", "callback_data": cbSkills},
				{"text": "🔄 Обновить", "callback_data": cbRefresh},
			},
		},
	}
	raw, _ := json.Marshal(kb)
	return string(raw)
}

// menuText is the panel body: состояние, а не инструкция.
func menuText(cfg Config, chatID int64) string {
	var b strings.Builder
	b.WriteString("🎛 **Панель Kibborg**\n\n")
	b.WriteString(handsModeLabel(currentHandsMode()) + "\n")
	b.WriteString(ttsModeShort(currentTTSMode()) + "\n\n")

	if t := activeTask(chatID); t != nil && t.GetStatus() == TaskRunning {
		b.WriteString("⚙️ Сейчас выполняется: «" + capAgentText(t.Input, 60) + "»\n")
	} else {
		b.WriteString("💤 Активных задач нет.\n")
	}
	ctxInfo := contextSnapshot(cfg, chatID)
	total, _ := ctxInfo["total"].(int)
	used, _ := ctxInfo["used"].(int)
	msgs, _ := ctxInfo["history_msgs"].(int)
	if total > 0 {
		line := fmt.Sprintf("🧠 Контекст: %d из %d токенов", used, total)
		if pct, ok := ctxInfo["pct"].(int); ok {
			line += fmt.Sprintf(" (%d%%)", pct)
		}
		b.WriteString(line + fmt.Sprintf(", реплик в памяти: %d\n", msgs))
	}
	b.WriteString("\nКнопки ниже работают даже во время выполнения задачи.")
	return b.String()
}

// sendTelegramMenu posts a fresh panel.
func sendTelegramMenu(botAPI string, cfg Config, chatID int64) {
	sendTelegramWithMarkup(botAPI, chatID, menuText(cfg, chatID), menuKeyboard())
}

// ===== обработка нажатий =====

// handleCallbackQuery обрабатывает нажатие кнопки. Возвращать нечего: ответ уходит в чат
// сразу, а сама кнопка «отпускается» через answerCallbackQuery (иначе Telegram крутит
// часики на клиенте до таймаута).
func handleCallbackQuery(cfg Config, botAPI string, allow map[int64]bool, cb *tgCallbackQuery) {
	chatID := int64(0)
	messageID := 0
	if cb.Message != nil {
		chatID = cb.Message.Chat.ID
		messageID = cb.Message.MessageID
	}
	if chatID == 0 {
		chatID = cb.From.ID
	}
	// Та же проверка, что в handledPreQueue: панель управляет ЧУЖИМ компьютером, и кнопка не
	// может быть слабее команды, которую она заменяет.
	if !isOwnerChat(allow, chatID) {
		answerCallback(botAPI, cb.ID, "Только владелец (TELEGRAM_ID)", true)
		return
	}

	switch cb.Data {
	case cbHandsFull, cbHandsSafe:
		want := handsModeSafe
		if cb.Data == cbHandsFull {
			want = handsModeFull
		}
		mode := setHandsMode(want, fmt.Sprintf("telegram-menu:%d", chatID))
		answerCallback(botAPI, cb.ID, handsModeShort(mode), false)
		editTelegramMarkup(botAPI, chatID, messageID, menuText(cfg, chatID), menuKeyboard())
		sendTelegramMessage(botAPI, chatID, handsModeLabel(mode))

	case cbTTSAuto, cbTTSAsk:
		want := ttsModeAsk
		if cb.Data == cbTTSAuto {
			want = ttsModeAuto
		}
		mode := setTTSMode(want, fmt.Sprintf("telegram-menu:%d", chatID))
		answerCallback(botAPI, cb.ID, ttsModeShort(mode), false)
		editTelegramMarkup(botAPI, chatID, messageID, menuText(cfg, chatID), menuKeyboard())
		sendTelegramMessage(botAPI, chatID, ttsModeLabel(mode))

	case cbSpeak:
		src := takeLastSpeakable(chatID)
		if src == "" {
			answerCallback(botAPI, cb.ID, "Нечего озвучивать", false)
			return
		}
		answerCallback(botAPI, cb.ID, "Читаю…", false)
		log.Printf("[TTS] telegram кнопка «Озвучить», исходник %d символов", utf8.RuneCountInString(src))
		sf, err := synthesizeSpeech(cfg, src)
		if err != nil {
			log.Printf("[TTS] telegram кнопка: %v", err)
			sendTelegramMessage(botAPI, chatID, "❌ "+err.Error())
			return
		}
		if err := sendTelegramVoiceFile(botAPI, chatID, telegramVoicePath(sf)); err != nil {
			sendTelegramMessage(botAPI, chatID, "❌ Не отправился голос: "+err.Error())
		}

	case cbStop:
		if taskID, ok := stopActiveTask(chatID); ok {
			answerCallback(botAPI, cb.ID, "Остановлено", false)
			log.Printf("[TELEGRAM] кнопка «Стоп» → задача %s остановлена", taskID)
			sendTelegramMessage(botAPI, chatID, "⏹ Остановлено.")
		} else {
			answerCallback(botAPI, cb.ID, "Нечего останавливать", false)
		}
		editTelegramMarkup(botAPI, chatID, messageID, menuText(cfg, chatID), menuKeyboard())

	case cbCompact:
		answerCallback(botAPI, cb.ID, "Сжимаю…", false)
		stop := startTyping(botAPI, chatID)
		reply := handleCompactCommand(cfg, chatID)
		stop()
		sendTelegramMessage(botAPI, chatID, reply)
		editTelegramMarkup(botAPI, chatID, messageID, menuText(cfg, chatID), menuKeyboard())

	case cbReset:
		resetChatContext(chatID)
		answerCallback(botAPI, cb.ID, "Контекст очищен", false)
		sendTelegramMessage(botAPI, chatID, "♻️ Контекст диалога и долговременная память очищены.")
		editTelegramMarkup(botAPI, chatID, messageID, menuText(cfg, chatID), menuKeyboard())

	case cbStatus:
		answerCallback(botAPI, cb.ID, "", false)
		sendTelegramMessage(botAPI, chatID, telegramStatusText(cfg, chatID))

	case cbSkills:
		answerCallback(botAPI, cb.ID, "", false)
		sendTelegramMessage(botAPI, chatID, skillsText())

	case cbRefresh:
		answerCallback(botAPI, cb.ID, "Обновлено", false)
		editTelegramMarkup(botAPI, chatID, messageID, menuText(cfg, chatID), menuKeyboard())

	default:
		answerCallback(botAPI, cb.ID, "Неизвестная кнопка", false)
	}
}

// resetChatContext is the shared body of /reset and the panel button.
func resetChatContext(chatID int64) {
	histMu.Lock()
	delete(history, chatID)
	markHistoryDirty()
	histMu.Unlock()
	takeChartPending(chatID)
	forgetMemory(chatID)
	clearAgentURLs(chatID)
	// Счётчик контекста — это замер последнего запроса. История стёрта, значит замер больше
	// ничего не описывает: обнуляем, иначе панель показывает занятость несуществующего диалога.
	live.forgetContext()
}

// telegramStatusText mirrors the Web «Статус» tab in one message.
func telegramStatusText(cfg Config, chatID int64) string {
	var b strings.Builder
	b.WriteString("🩺 **Состояние**\n")
	if brainReady(cfg.BrainPort) {
		b.WriteString("🧠 Мозг: готов, порт " + strconv.Itoa(cfg.BrainPort) + "\n")
	} else {
		b.WriteString("🧠 Мозг: ещё грузится\n")
	}
	ctxInfo := contextSnapshot(cfg, chatID)
	if total, _ := ctxInfo["total"].(int); total > 0 {
		used, _ := ctxInfo["used"].(int)
		line := fmt.Sprintf("🪟 Контекст: %d / %d токенов", used, total)
		if pct, ok := ctxInfo["pct"].(int); ok {
			line += fmt.Sprintf(" (%d%%)", pct)
		}
		b.WriteString(line + "\n")
	}
	if msgs, _ := ctxInfo["history_msgs"].(int); msgs > 0 {
		tok, _ := ctxInfo["history_tokens"].(int)
		b.WriteString(fmt.Sprintf("💬 История чата: %d реплик ≈ %d токенов (сжать: /compact)\n", msgs, tok))
	}
	snap := live.snapshot()
	if last, ok := snap["last"].(map[string]any); ok {
		if has, _ := last["has"].(bool); has {
			b.WriteString(fmt.Sprintf("⚡ Последняя генерация: %v ток/с, промпт %v ток/с\n",
				last["gen_tok_s"], last["prompt_tok_s"]))
		}
	}
	if tabs, err := getBrowserSession().ListTabs(); err == nil {
		b.WriteString(fmt.Sprintf("🌐 Chrome: %d вкладок\n", len(tabs)))
	} else {
		b.WriteString("🌐 Chrome: не подключён (нужен --remote-debugging-port)\n")
	}
	if desktopSupported() {
		if mons := listMonitors(); len(mons) > 0 {
			b.WriteString(fmt.Sprintf("💻 Мониторов: %d (скриншот: «сделай скриншот экрана»)\n", len(mons)))
		}
	}
	b.WriteString(handsModeShort(currentHandsMode()) + " руки")
	return b.String()
}

// skillsText — ответ кнопки «Что я умею». Он ЗАХАРДКОЖЕН намеренно: на живом прогоне тот же
// вопрос, заданный модели, дал реферат о том, чего она якобы не может (скриншот рабочего
// стола, мышь, запуск программ) — всё это неправдой было уже тогда. Список возможностей —
// факт о движке, и брать его надо из движка.
func skillsText() string {
	return "🧰 **Что я умею**\n\n" +
		"💻 **Компьютер**: команды в PowerShell, любые файлы на любом диске, процессы, " +
		"запуск программ, скриншот экрана и окон, клавиатура и мышь, буфер обмена.\n" +
		"🌐 **Интернет**: поиск, чтение страниц, HTTP-запросы, GitHub, субтитры YouTube.\n" +
		"🧭 **Chrome**: вкладки, чтение DOM и таблиц, клики, формы, загрузка файлов, скриншот страницы.\n" +
		"📊 **Рынок**: разбор тикера по свечам Binance, размер позиции по риску, журнал сделок.\n" +
		"🧩 **Модели**: тест железа, каталог GGUF, прогресс скачивания и переключение (/hw, /models, /models use).\n" +
		"🔊 **Озвучка**: Qwen3-TTS читает ответ вслух — `/tts auto` всегда, `/speak` по запросу.\n" +
		"🛡 **Безопасность**: разбор логов, поиск IOC, хеши и энтропия файла.\n" +
		"🎬 **Видео**: пришли ролик или ссылку — вытащу речь в текст (любой длины), посмотрю кадры, " +
		"расскажу содержание и найду по нему что нужно. Плюс скачивание, субтитры, конвертация и нарезка.\n" +
		"👁 **Зрение**: разбор картинок и скриншотов графиков.\n" +
		"🎙 **Голос**: распознаю голосовые и отвечаю как на текст. Ответы читаю вслух — Qwen3-TTS, кнопка «Озвучить» или `/speak`.\n" +
		"🧠 **Память**: помню прошлые диалоги, умею сжимать контекст (/compact).\n\n" +
		"Границы ставит только режим рук: " + handsModeShort(currentHandsMode()) + " — /menu переключает."
}

// ===== HTTP-обвязка =====

func telegramPostJSON(botAPI, method string, body []byte) error {
	resp, err := tgHTTP.Post(botAPI+"/"+method, "application/json", strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("%s", redactErr(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("%s HTTP %d", method, resp.StatusCode)
	}
	return nil
}

// sendTelegramWithMarkup posts a message carrying an inline keyboard.
func sendTelegramWithMarkup(botAPI string, chatID int64, text, markup string) {
	form := url.Values{}
	form.Set("chat_id", strconv.FormatInt(chatID, 10))
	form.Set("text", toTelegramHTML(redact(stripThink(text))))
	form.Set("parse_mode", "HTML")
	form.Set("reply_markup", markup)
	if resp, err := tgHTTP.PostForm(botAPI+"/sendMessage", form); err == nil {
		resp.Body.Close()
	} else {
		log.Printf("[TELEGRAM] меню не отправилось: %s", redactErr(err))
	}
}

// editTelegramMarkup refreshes the panel in place, so the chat does not fill with copies of it.
func editTelegramMarkup(botAPI string, chatID int64, messageID int, text, markup string) {
	if messageID == 0 {
		return
	}
	form := url.Values{}
	form.Set("chat_id", strconv.FormatInt(chatID, 10))
	form.Set("message_id", strconv.Itoa(messageID))
	form.Set("text", toTelegramHTML(redact(stripThink(text))))
	form.Set("parse_mode", "HTML")
	form.Set("reply_markup", markup)
	if resp, err := tgHTTP.PostForm(botAPI+"/editMessageText", form); err == nil {
		resp.Body.Close()
	}
}

// answerCallback releases the button's spinner on the client.
func answerCallback(botAPI, callbackID, text string, alert bool) {
	form := url.Values{}
	form.Set("callback_query_id", callbackID)
	if text != "" {
		form.Set("text", text)
	}
	if alert {
		form.Set("show_alert", "true")
	}
	if resp, err := tgHTTP.PostForm(botAPI+"/answerCallbackQuery", form); err == nil {
		resp.Body.Close()
	}
}
