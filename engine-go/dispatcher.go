package main

// Layer 1 — the dispatcher (ТЗ §4.1). Same brain, no tools, one short prompt: it reads the
// request (plus 2–3 turns of history, the URL bag and the memory summary) and answers with
// JSON: which packs, what the plan is, whether a confirmation is likely, and one line for
// the human.
//
// It is NOT a security boundary (§4). Routing decides what is convenient; guardToolCall and
// the channel allowlist decide what is permitted.

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"kibborg/engine/browser"
)

// dispatchPlan is the dispatcher's contract. Every field has a consumer (§4.1) — a field
// nobody reads is a field that quietly rots.
type dispatchPlan struct {
	Packs   []string `json:"packs"`   // → layer 2, schema assembly
	Plan    []string `json:"plan"`    // → executor prompt AND the UI status ribbon
	Confirm bool     `json:"confirm"` // → a heads-up to the human. NOT a gate
	Summary string   `json:"summary"` // → first line of the answer
	Raw     string   `json:"-"`       // → tasks.jsonl, for "why did it route there?"
}

// dispatcherHistoryTurns / Chars keep the dispatcher prompt short: its own call is cheap,
// and a short prompt is the only part of the KV story we actually control (§3.2).
const (
	dispatcherHistoryTurns = 3
	dispatcherHistoryChars = 300
	dispatcherMemChars     = 500
)

// dispatcherSystemPrompt is built per call: the pack table and few-shots are the domain, and
// the web↔trade pair is the split the model gets wrong most often.
func dispatcherSystemPrompt() string {
	now := time.Now()
	return `Ты — МАРШРУТИЗАТОР локального ИИ-агента Kibborg. Инструменты ты НЕ вызываешь.
Твоя работа: прочитать запрос и вернуть ТОЛЬКО JSON — какие наборы инструментов («паки») нужны исполнителю.

Формат ответа (строго JSON, без пояснений и без markdown-заборов):
{"packs":["web"],"plan":["найти 3 статьи","прочитать"],"confirm":false,"summary":"найду и перескажу"}

Паки:
` + packsSummaryForPrompt() + `

Правила выбора:
- болтовня, «привет», «помнишь, о чём просил» → chat
- новости, факты, «что сейчас», «что там с…», цены и события → web
- вкладка / клик / форма / «на открытой странице» → browser.read (+ browser.act, если надо нажимать)
- запустить команду, посмотреть процессы, git → console
- пути, «сохрани в файл», «прочитай файл», «покажи каталог» → files
- скачать ролик, субтитры, «озвучь», «скажи голосом» → media
- ЯВНЫЙ разбор тикера, график, сайзинг, журнал сделки → trade
- уровни, ATR, стоп, тейк, точка входа, ложный пробой, «по Герчику», «можно ли входить» → trade
  (это разбор инструмента по свечам, а НЕ новости: цифры даёт analyze_ticker)
- логи бота, IOC, аудит файла → secops
- скриншот экрана/рабочего стола, «что у меня открыто», окна, мышь, клавиатура, «нажми», «запусти программу», буфер обмена → system
- во входе есть блок «[Разбор приложенного …]» — вложение УЖЕ превращено в текст до тебя.
  Ответ на вопрос виден прямо в этом тексте → chat. Инструменты нужны, только если нужного
  в разборе НЕТ (поискать в интернете, открыть страницу, прочитать другой файл)

Примеры (это основной домен, следуй им точно):
- «Что сегодня по BTC?» → {"packs":["web"],...}            (новости/факты, НЕ стакан)
- «что там с эфиром?» → {"packs":["web"],...}              (серая фраза = новости)
- «разбери ETH» / «разбери эфир» → {"packs":["trade"],...} (явный разбор)
- «залогируй лонг BTC от 108к» → {"packs":["trade"],...}   (журнал)
- «разбери BTC по Герчику» / «где уровни по эфиру» / «какой стоп по BTC» → {"packs":["trade"],...}
- «найди статьи и сохрани выжимку в md» → {"packs":["web","files"],...}
- «что за монета BRU, кто создал, какая ликвидность» → {"packs":["web"],...} (данные о проекте ищутся в интернете, а не в стакане Binance)
- «посмотри в интернете», «поищи хорошо», «нужны полные данные» → {"packs":["web"],...}
- «какие у тебя есть инструменты / что ты умеешь» → {"packs":["chat"],...} (исполнитель сам расскажет про весь арсенал)
- «сделай скриншот рабочего стола» → {"packs":["system"],...} (экран ПК, а НЕ вкладка браузера)
- «что у меня сейчас открыто» → {"packs":["system"],...}
- «открой блокнот и напиши туда текст» → {"packs":["system"],...}
- «какая сумма в акте» + [Разбор приложенного документа] → {"packs":["chat"],...} (текст уже перед тобой)

Поля:
- packs — массив имён из списка выше (1–3 штуки, лишнего не бери)
- plan — 1–4 коротких шага по-русски
- confirm — true, если действие может потребовать подтверждения (удаление, запись вне рабочих папок, установка чего-либо)
- summary — одна короткая фраза от первого лица, что ты сейчас сделаешь

Сейчас: ` + now.Format("02.01.2006 15:04") + ` (` + now.Location().String() + `).
Верни ТОЛЬКО JSON-объект.`
}

// runDispatcher asks the brain to route the request. It never fails hard: a broken answer
// falls back to heuristics (§4.1) so the user always gets an agent, not an error.
func runDispatcher(cfg Config, t *Task, history []map[string]any, memSummary string, hintPacks []string) dispatchPlan {
	start := time.Now()
	defer func() { t.DispatcherMs = time.Since(start).Milliseconds() }()

	msgs := []map[string]any{{"role": "system", "content": dispatcherSystemPrompt()}}
	if ctxBlock := dispatcherContext(t.ChatID, history, memSummary); ctxBlock != "" {
		msgs = append(msgs, map[string]any{"role": "system", "content": ctxBlock})
	}
	userMsg := capAgentText(t.Input, 1500)
	if len(hintPacks) > 0 {
		userMsg += "\n\n(Пользователь пришёл через команду, скорее всего нужен пак: " +
			strings.Join(hintPacks, ", ") + ". Это подсказка — решай сам.)"
	}
	msgs = append(msgs, map[string]any{"role": "user", "content": userMsg})

	var lastRaw string
	for attempt := 0; attempt < 2; attempt++ {
		if err := t.Context().Err(); err != nil {
			return fallbackPlan(t.Input, hintPacks)
		}
		reply, _, err := llmChatTools(t.Context(), cfg.BrainPort, msgs, nil, 0.2)
		if err != nil {
			log.Printf("[DISPATCH] попытка %d: %v", attempt+1, err)
			continue
		}
		lastRaw = strings.TrimSpace(stripThink(reply.Content))
		plan, ok := parseDispatchJSON(lastRaw)
		if ok {
			plan.Raw = capAgentText(lastRaw, 600)
			return plan
		}
		log.Printf("[DISPATCH] не разобрал JSON (попытка %d): %s", attempt+1, capAgentText(lastRaw, 200))
		msgs = append(msgs,
			map[string]any{"role": "assistant", "content": lastRaw},
			map[string]any{"role": "user", "content": "Это не JSON. Верни ТОЛЬКО объект вида " +
				`{"packs":["web"],"plan":["…"],"confirm":false,"summary":"…"} — без текста вокруг.`})
	}

	fb := fallbackPlan(t.Input, hintPacks)
	fb.Raw = "fallback: " + capAgentText(lastRaw, 200)
	return fb
}

// dispatcherContext is the small context block: recent turns, remembered URLs, memory digest.
// History matters for follow-ups («сохрани это», «прочитай подробнее») — without it the
// dispatcher routes «сохрани это» to chat and the chain dies (§4.1).
func dispatcherContext(chatID int64, history []map[string]any, memSummary string) string {
	var b strings.Builder
	turns := recentTurns(history, dispatcherHistoryTurns)
	if len(turns) > 0 {
		b.WriteString("Последние реплики диалога (для follow-up вроде «сохрани это»):\n")
		for _, m := range turns {
			role := "Пользователь"
			if r, _ := m["role"].(string); r == "assistant" {
				role = "Ты"
			}
			b.WriteString("- " + role + ": " + capAgentText(msgContentString(m["content"]), dispatcherHistoryChars) + "\n")
		}
	}
	if urls := getAgentURLs(chatID); len(urls) > 0 {
		b.WriteString("URL из прошлого поиска (значит, «прочитай подробнее» — это про них): ")
		if len(urls) > 3 {
			urls = urls[len(urls)-3:]
		}
		b.WriteString(strings.Join(urls, " ") + "\n")
	}
	if s := strings.TrimSpace(memSummary); s != "" {
		b.WriteString("Сводка долговременной памяти: " + capAgentText(s, dispatcherMemChars) + "\n")
	}
	return strings.TrimSpace(b.String())
}

// recentTurns takes the last n user/assistant messages from a base message list.
func recentTurns(history []map[string]any, n int) []map[string]any {
	var turns []map[string]any
	for _, m := range history {
		role, _ := m["role"].(string)
		if role != "user" && role != "assistant" {
			continue
		}
		if strings.TrimSpace(msgContentString(m["content"])) == "" {
			continue
		}
		turns = append(turns, m)
	}
	if len(turns) > n {
		turns = turns[len(turns)-n:]
	}
	return turns
}

// parseDispatchJSON extracts the JSON object even when the model wrapped it in prose or a
// ```json fence, and validates pack names against the whitelist.
func parseDispatchJSON(raw string) (dispatchPlan, bool) {
	body := extractJSONObject(raw)
	if body == "" {
		return dispatchPlan{}, false
	}
	var parsed struct {
		Packs   []string `json:"packs"`
		Plan    []string `json:"plan"`
		Confirm any      `json:"confirm"`
		Summary string   `json:"summary"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return dispatchPlan{}, false
	}
	if len(parsed.Packs) == 0 {
		return dispatchPlan{}, false
	}
	plan := dispatchPlan{
		Packs:   normalizePacks(parsed.Packs),
		Plan:    trimPlan(parsed.Plan),
		Summary: strings.TrimSpace(parsed.Summary),
	}
	switch c := parsed.Confirm.(type) {
	case bool:
		plan.Confirm = c
	case string:
		plan.Confirm = strings.EqualFold(strings.TrimSpace(c), "true")
	}
	return plan, true
}

// extractJSONObject finds the outermost {...} in a reply, tolerating fences and commentary.
func extractJSONObject(s string) string {
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start < 0 || end <= start {
		return ""
	}
	return s[start : end+1]
}

func trimPlan(in []string) []string {
	out := make([]string, 0, len(in))
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, capAgentText(p, 120))
		if len(out) == 4 {
			break
		}
	}
	return out
}

// ===== fallback (§4.1) =====

// fallbackPlan is used when the dispatcher returned garbage twice. The bias is deliberate:
// toward `web`, not `chat` — answering a factual question from the model's head is the exact
// failure this whole design exists to prevent. `chat` only for a plain greeting.
func fallbackPlan(input string, hintPacks []string) dispatchPlan {
	text := strings.TrimSpace(input)
	lower := strings.ToLower(text)

	// A slash command's hint is a stronger signal than any heuristic: the user literally
	// named the capability.
	if packs := normalizePacks(hintPacks); len(hintPacks) > 0 && packs[0] != packChat {
		return dispatchPlan{Packs: packs, Summary: "выполняю запрос"}
	}

	if browser.ExtractVideoURL(text) != "" {
		// Голая ссылка (или прямая просьба скачать) — это скачивание. Ссылка со словами
		// «разбери / о чём / что он говорит» — это РАЗБОР: слать такое в web_search
		// бессмысленно, содержание ролика поиском не достаётся.
		if len(strings.Fields(text)) <= 3 || looksLikeDownloadRequest(text) {
			return dispatchPlan{
				Packs: []string{packMedia}, Confirm: false,
				Summary: "скачаю видео",
				Plan:    []string{"скачать видео по ссылке"},
			}
		}
		return dispatchPlan{
			Packs: []string{packMedia}, Confirm: false,
			Summary: "разберу видео",
			Plan:    []string{"analyze_video по ссылке", "ответ по расшифровке"},
		}
	}
	if mentionsMediaFile(lower) {
		return dispatchPlan{
			Packs: []string{packMedia}, Confirm: false,
			Summary: "разберу файл",
			Plan:    []string{"analyze_video по пути к файлу"},
		}
	}
	if looksLikeTickerOnly(text) {
		return dispatchPlan{
			Packs: []string{packTrade}, Confirm: false,
			Summary: "разберу тикер",
			Plan:    []string{"детерминированный разбор по Binance"},
		}
	}
	if looksLikePureGreeting(lower) {
		return dispatchPlan{
			Packs: []string{packChat}, Confirm: false,
			Summary: "отвечу словами",
		}
	}
	return dispatchPlan{
		Packs: []string{packWeb}, Confirm: false,
		Summary: "поищу и прочитаю источники",
		Plan:    []string{"web_search по запросу", "read_url на лучшие результаты", "ответ со ссылками"},
	}
}

// mentionsMediaFile ловит путь к видео/аудио в тексте («разбери D:\лекция.mp4»). Нужен только
// для аварийного плана: без него такой запрос уходил в web_search, то есть в поиск в интернете
// файла, который лежит на этом же диске.
func mentionsMediaFile(lower string) bool {
	for _, ext := range []string{
		".mp4", ".mov", ".mkv", ".avi", ".webm", ".m4v", ".wmv", ".mpeg", ".mpg",
		".mp3", ".wav", ".m4a", ".flac", ".ogg", ".oga", ".opus", ".aac",
	} {
		if strings.Contains(lower, ext) {
			return true
		}
	}
	return false
}

// looksLikeTickerOnly: the WHOLE message is a ticker ± one word («разбери ETH», «BTC»).
func looksLikeTickerOnly(text string) bool {
	fields := strings.Fields(text)
	if len(fields) == 0 || len(fields) > 2 {
		return false
	}
	for _, f := range fields {
		if isTickerToken(f) {
			return true
		}
	}
	return false
}

var knownTickerWords = map[string]bool{
	"btc": true, "eth": true, "sol": true, "bnb": true, "xrp": true, "ada": true,
	"doge": true, "ton": true, "avax": true, "link": true, "dot": true, "matic": true,
	"bitcoin": true, "ethereum": true, "solana": true,
	"биток": true, "битка": true, "эфир": true, "эфира": true, "солана": true, "биткоин": true,
}

func isTickerToken(tok string) bool {
	t := strings.ToLower(strings.Trim(tok, ".,!?/\\"))
	if knownTickerWords[t] {
		return true
	}
	// BTCUSDT-style: all-latin uppercase, 5–12 chars, ends with a quote asset.
	up := strings.ToUpper(t)
	return len(up) >= 5 && len(up) <= 12 &&
		(strings.HasSuffix(up, "USDT") || strings.HasSuffix(up, "USDC")) &&
		up == strings.ToUpper(tok)
}

func looksLikePureGreeting(lower string) bool {
	l := strings.Trim(strings.TrimSpace(lower), " .!?")
	switch l {
	case "привет", "здорова", "здравствуй", "здравствуйте", "хай", "hi", "hello", "йо",
		"как дела", "как ты", "спасибо", "пока", "доброе утро", "добрый день", "добрый вечер":
		return true
	}
	return false
}

// planForPrompt renders the dispatcher's steps into the executor's system prompt (§4.1).
func planForPrompt(p dispatchPlan) string {
	if len(p.Plan) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nНамеченные шаги (ориентир, не догма — если данные говорят иначе, действуй по данным):")
	for i, step := range p.Plan {
		fmt.Fprintf(&b, "\n%d. %s", i+1, step)
	}
	return b.String()
}
