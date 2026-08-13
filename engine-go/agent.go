package main

// General-purpose tool agent: the local LLM picks tools (terminal, files, web search,
// browser, Agent Reach…), we execute them, feed results back, and loop until a final answer.
// Used by /browser, /agent, and (when enabled) ordinary chat that needs real-world actions.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"kibborg/engine/browser"
)

// agentSystemPromptBody is the static arsenal guide. Live clock is appended every call
// via agentSystemPrompt() so the model never guesses the date from training data.
// The pack-specific halves (the news pipeline, the browser tab rule, the trade numbers rule)
// are appended per task by executorSystemPrompt — the model should read rules for the hands
// it actually has, not for the whole arsenal.
const agentSystemPromptBody = `Ты — Kibborg Agent: локальный ИИ-помощник С РЕАЛЬНЫМИ ИНСТРУМЕНТАМИ.
Отвечай по-русски.

══════════════════════════════════════
⛔ КРИТИЧЕСКИ ВАЖНО (нарушение = ошибка)
══════════════════════════════════════
1. Инструменты у тебя настоящие (run_command, web_search, read_url и др. — по активным пакам).
   НИКОГДА не говори «нет доступа к интернету/консоли».
2. Дата/время → блок «СЕЙЧАС» ниже или run_command (Get-Date / date). Не угадывай из обучения.
3. Нет данных от инструментов → скажи «не нашёл». Не сочиняй факты, числа и ссылки.
4. Инструмент вернул статус denied / blocked → это решение системы безопасности, а не сбой:
   не повторяй тот же вызов, предложи альтернативу.
   Статус timeout → состояние неизвестно, проверь результат, а не повторяй вслепую.
5. Отвечай человеку по делу: что сделал, что получилось, откуда данные.

Формат ответа на новости/факты: список пунктов «- **Факт** … Источник: [заголовок](url)».
Без воды и без «рекомендую мониторить эти сайты» вместо фактов.`

// agentSystemPrompt returns the full system prompt with a live local clock line.
func agentSystemPrompt() string {
	now := time.Now()
	// Russian weekday via fixed map (avoid locale deps).
	wd := []string{"воскресенье", "понедельник", "вторник", "среда", "четверг", "пятница", "суббота"}[int(now.Weekday())]
	clock := fmt.Sprintf(
		"\n\n══════════════════════════════════════\n⏱ СЕЙЧАС (часы этого ПК, источник истины для даты/времени)\n══════════════════════════════════════\n"+
			"%s, %s\nЧасовой пояс: %s\nUnix: %d\n"+
			"Если нужна проверка — run_command: Get-Date -Format o   (Windows) или date -Iseconds (Linux/macOS).",
		wd,
		now.Format("02.01.2006 15:04:05"),
		now.Location().String(),
		now.Unix(),
	)
	return agentSystemPromptBody + clock
}

const (
	maxAgentSteps = 12
	// Keep agent prompts inside a 32k context: chat templates expand tool schemas heavily.
	agentMaxHistMsgs  = 6    // last N user/assistant turns (excl. system)
	agentMaxMsgChars  = 3500 // per history/memory message
	agentMaxToolChars = 6000 // per tool result fed back to the model
	agentMaxMemBlocks = 1    // at most one memory system block
	agentMaxMemChars  = 1500
	// Soft budget for the whole tool-loop prompt (chars). Leave headroom for generation
	// and chat-template tool expansion under LLAMA_CTX_SIZE=32768.
	agentSoftBudgetChars = 48_000
)

var (
	browserMu      sync.Mutex
	browserSession *browser.Session
)

// browserTaskMu serializes whole agent tasks (shared Chrome session + artifact buffer).
var browserTaskMu sync.Mutex

// Last URLs found by tools for each chat — so follow-ups like «прочитай всё / выжимка»
// don't lose the search hits (history only stores final prose, not tool JSON).
var (
	agentURLMu  sync.Mutex
	agentURLBag = map[int64][]string{}
)

const agentURLBagMax = 12

func getBrowserSession() *browser.Session {
	return getBrowserSessionWithFFmpeg("")
}

func getBrowserSessionWithFFmpeg(ffmpegPath string) *browser.Session {
	browserMu.Lock()
	defer browserMu.Unlock()
	if browserSession == nil {
		browserSession = browser.New("")
	}
	if ffmpegPath != "" {
		browserSession.FFmpegPath = ffmpegPath
	}
	return browserSession
}

func closeBrowserSession() {
	browserMu.Lock()
	defer browserMu.Unlock()
	if browserSession != nil {
		browserSession.Close()
		browserSession = nil
	}
}

// runAgent is THE entry point for both channels: text or voice, slash or free chat, all go
// through the dispatcher (§4). Slash commands are hints, not a bypass.
func runAgent(cfg Config, actor Actor, baseMsgs []map[string]any, task string, status func(string)) agentResult {
	return runLayeredAgent(agentRequest{
		cfg:        cfg,
		actor:      actor,
		baseMsgs:   baseMsgs,
		input:      task,
		status:     status,
		memSummary: memorySummaryFor(actor.ChatID),
	})
}

// readFollowUpHint is appended to the user's text when they ask for a digest of what was
// already found: the URL bag is the answer, not another request for links.
func readFollowUpHint(chatID int64, task string) string {
	if !looksLikeReadFollowUp(task) {
		return task
	}
	urls := getAgentURLs(chatID)
	if len(urls) == 0 {
		return task + "\n\n(Сначала web_search по теме, затем read_url на 3–5 конкретных статей из выдачи, " +
			"затем выжимка с URL. НЕ проси пользователя прислать ссылки.)"
	}
	return task + "\n\n(Сделай выжимку: вызови read_url на эти URL по очереди, затем список фактов " +
		"с markdown-ссылкой на каждый источник. НЕ проси меня прислать ссылки:\n" + strings.Join(urls, "\n") + ")"
}

// shrinkAgentMsgs drops history/memory and optionally keeps only system + task + last tool pair.
func shrinkAgentMsgs(msgs []map[string]any, sys, task string, keepLastTools bool) []map[string]any {
	out := []map[string]any{{"role": "system", "content": sys}}
	if keepLastTools && len(msgs) > 2 {
		// Keep last assistant(tool_calls)+tool results (up to 6 msgs).
		start := len(msgs) - 6
		if start < 1 {
			start = 1
		}
		tail := compactToolMessages(msgs[start:])
		for _, m := range tail {
			role, _ := m["role"].(string)
			if role == "system" {
				continue
			}
			out = append(out, m)
		}
	}
	// Ensure the original task is still present if not already last user msg.
	if strings.TrimSpace(task) != "" {
		needTask := true
		for _, m := range out {
			if role, _ := m["role"].(string); role == "user" {
				if c, _ := m["content"].(string); strings.Contains(c, task) || c == task {
					needTask = false
					break
				}
			}
		}
		if needTask {
			out = append(out, map[string]any{"role": "user", "content": task})
		}
	}
	return out
}

func slimForSummary(msgs []map[string]any, sys string) []map[string]any {
	out := []map[string]any{{"role": "system", "content": sys}}
	if len(msgs) <= 1 {
		return out
	}
	// Prefer tool results + short assistant notes from the loop.
	var picked []map[string]any
	for _, m := range msgs[1:] {
		role, _ := m["role"].(string)
		switch role {
		case "tool":
			c := capAgentText(msgContentString(m["content"]), 1500)
			picked = append(picked, map[string]any{
				"role":         "tool",
				"tool_call_id": m["tool_call_id"],
				"name":         m["name"],
				"content":      c,
			})
		case "assistant":
			// Skip raw tool_calls blobs in summary — only free text if any.
			if _, has := m["tool_calls"]; has {
				continue
			}
			c := capAgentText(msgContentString(m["content"]), 800)
			if c != "" {
				picked = append(picked, map[string]any{"role": "assistant", "content": c})
			}
		case "user":
			c := capAgentText(msgContentString(m["content"]), 500)
			if c != "" {
				picked = append(picked, map[string]any{"role": "user", "content": c})
			}
		}
	}
	if len(picked) > 8 {
		picked = picked[len(picked)-8:]
	}
	// Summary without tools: convert tool roles to plain user notes (cleaner for no-tools call).
	for _, m := range picked {
		role, _ := m["role"].(string)
		if role == "tool" {
			name, _ := m["name"].(string)
			out = append(out, map[string]any{
				"role":    "user",
				"content": "Результат " + name + ":\n" + msgContentString(m["content"]),
			})
			continue
		}
		out = append(out, m)
	}
	return out
}

// estimateAgentChars is a rough size of messages+tools JSON (not real tokens, but enough
// to stay under 32k when chat templates expand schemas ~2–3×).
func estimateAgentChars(msgs []map[string]any, tools any) int {
	n := 0
	for _, m := range msgs {
		b, _ := json.Marshal(m)
		n += len(b)
	}
	if tools != nil {
		b, _ := json.Marshal(tools)
		// Tool schemas are expanded heavily by jinja chat templates.
		n += len(b) * 3
	}
	return n
}

// packAgentMessages builds a tight message list: live agent system + optional 1 memory block
// + last few history turns (capped) + current user task. Drops the default chat system prompt.
func packAgentMessages(base []map[string]any, sys, task string) []map[string]any {
	out := make([]map[string]any, 0, 12)
	out = append(out, map[string]any{"role": "system", "content": sys})

	if len(base) > 1 {
		var mem []map[string]any
		var hist []map[string]any
		for _, m := range base[1:] {
			role, _ := m["role"].(string)
			content := msgContentString(m["content"])
			if role == "system" {
				// Memory / injected system blocks — keep at most one, capped.
				if len(mem) >= agentMaxMemBlocks {
					continue
				}
				content = capAgentText(content, agentMaxMemChars)
				if content == "" {
					continue
				}
				mem = append(mem, map[string]any{"role": "system", "content": content})
				continue
			}
			if role != "user" && role != "assistant" {
				continue
			}
			content = capAgentText(content, agentMaxMsgChars)
			if content == "" {
				continue
			}
			hist = append(hist, map[string]any{"role": role, "content": content})
		}
		out = append(out, mem...)
		if len(hist) > agentMaxHistMsgs {
			hist = hist[len(hist)-agentMaxHistMsgs:]
		}
		out = append(out, hist...)
	}

	if strings.TrimSpace(task) != "" {
		out = append(out, map[string]any{"role": "user", "content": strings.TrimSpace(task)})
	}
	return out
}

func msgContentString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	default:
		return fmt.Sprint(v)
	}
}

func capAgentText(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

func isContextOverflow(err error) bool {
	return isContextBad(err)
}

// isContextBad matches llama-server failures around context size AND prompt-cache
// invalidation (log line: "erased invalidated context checkpoint").
func isContextBad(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return (strings.Contains(s, "exceed") && strings.Contains(s, "context")) ||
		strings.Contains(s, "exceed_context_size") ||
		strings.Contains(s, "context size") ||
		strings.Contains(s, "n_prompt_tokens") ||
		strings.Contains(s, "invalidat") ||
		strings.Contains(s, "invalid context") ||
		strings.Contains(s, "context checkpoint") ||
		strings.Contains(s, "kv cache") ||
		strings.Contains(s, "n_ctx")
}

// compactToolMessages shrinks older tool results, keeping the latest few intact.
func compactToolMessages(msgs []map[string]any) []map[string]any {
	// Count tool messages from the end; shrink all but the last 4 tool payloads.
	toolIdxs := make([]int, 0, 8)
	for i, m := range msgs {
		if role, _ := m["role"].(string); role == "tool" {
			toolIdxs = append(toolIdxs, i)
		}
	}
	if len(toolIdxs) <= 4 {
		return msgs
	}
	for _, i := range toolIdxs[:len(toolIdxs)-4] {
		if c, ok := msgs[i]["content"].(string); ok && len(c) > 400 {
			msgs[i]["content"] = capAgentText(c, 400)
		}
	}
	return msgs
}

// wantsToolAgent: free-text chat always uses the tool agent when tools are enabled for
// the channel (allowlist Telegram / localhost Web).
func wantsToolAgent(text string) bool {
	return strings.TrimSpace(text) != ""
}

// looksLikeNewsOrResearch is true when the task needs search+read, not a bare greeting.
func looksLikeNewsOrResearch(text string) bool {
	t := strings.ToLower(text)
	keys := []string{
		"новост", "news", "что происходит", "что случилось", "сегодня", "свеж",
		"последн", "рынок", "крипт", "bitcoin", "btc", "eth", "курс", "цена",
		"найди", "поищи", "search", "заголовк", "статья", "обзор", "дай данные",
		"с числами", "ликвидац", "etf", "рейтинг",
	}
	for _, k := range keys {
		if strings.Contains(t, k) {
			return true
		}
	}
	return false
}

// looksLikeReadFollowUp: user wants digests/sources from already-found or findable articles.
func looksLikeReadFollowUp(text string) bool {
	t := strings.ToLower(text)
	keys := []string{
		"прочитай", "прочти", "выжимк", "с источник", "со ссылк", "дай ссыл",
		"подробн", "разверн", "все новости", "прочитай все", "прочти все",
		"read all", "summary", "summar", "цитат", "по каждой",
	}
	for _, k := range keys {
		if strings.Contains(t, k) {
			return true
		}
	}
	return false
}

// shouldForceRead forces another tool step if the model tries to answer news without reading pages.
func shouldForceRead(task string, didSearch, didRead bool, step int) bool {
	if step >= 4 {
		return false // avoid infinite nudge loops
	}
	if didRead {
		return false
	}
	if looksLikeReadFollowUp(task) {
		return true
	}
	if looksLikeNewsOrResearch(task) && (didSearch || step < 2) {
		return true
	}
	return false
}

func rememberToolURLs(chatID int64, toolName, result, argsJSON string) {
	if chatID == 0 {
		return
	}
	var found []string
	found = append(found, extractHTTPURLs(result)...)
	found = append(found, extractHTTPURLs(argsJSON)...)
	if len(found) == 0 {
		return
	}
	agentURLMu.Lock()
	defer agentURLMu.Unlock()
	cur := agentURLBag[chatID]
	seen := map[string]bool{}
	for _, u := range cur {
		seen[u] = true
	}
	for _, u := range found {
		u = strings.TrimRight(u, ".,);]>\"'")
		if u == "" || seen[u] {
			continue
		}
		// Skip pure search engines / homepages noise somewhat
		low := strings.ToLower(u)
		if strings.Contains(low, "duckduckgo.com") || strings.Contains(low, "google.com/search") {
			continue
		}
		seen[u] = true
		cur = append(cur, u)
	}
	if len(cur) > agentURLBagMax {
		cur = cur[len(cur)-agentURLBagMax:]
	}
	agentURLBag[chatID] = cur
	_ = toolName
}

func getAgentURLs(chatID int64) []string {
	agentURLMu.Lock()
	defer agentURLMu.Unlock()
	src := agentURLBag[chatID]
	out := make([]string, len(src))
	copy(out, src)
	return out
}

func agentURLBagNote(chatID int64) string {
	urls := getAgentURLs(chatID)
	if len(urls) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("══════════════════════════════════════\n")
	b.WriteString("URL из прошлого поиска (используй read_url; НЕ проси пользователя)\n")
	b.WriteString("══════════════════════════════════════\n")
	for i, u := range urls {
		b.WriteString(fmt.Sprintf("%d. %s\n", i+1, u))
	}
	return b.String()
}

func clearAgentURLs(chatID int64) {
	agentURLMu.Lock()
	delete(agentURLBag, chatID)
	agentURLMu.Unlock()
}

// extractHTTPURLs pulls http(s) URLs from free text / JSON tool output.
func extractHTTPURLs(s string) []string {
	if s == "" {
		return nil
	}
	// Simple scanner: find http:// or https:// and take until whitespace/quote.
	var out []string
	for i := 0; i < len(s); {
		idx := strings.Index(s[i:], "http://")
		idxS := strings.Index(s[i:], "https://")
		start := -1
		if idx >= 0 && (idxS < 0 || idx < idxS) {
			start = i + idx
		} else if idxS >= 0 {
			start = i + idxS
		}
		if start < 0 {
			break
		}
		end := start
		for end < len(s) {
			c := s[end]
			if c <= ' ' || c == '"' || c == '\'' || c == '<' || c == '>' || c == ')' || c == ']' || c == '}' || c == ',' {
				break
			}
			end++
		}
		u := s[start:end]
		u = strings.TrimRight(u, ".,;:")
		if len(u) > 12 {
			out = append(out, u)
		}
		i = end
	}
	return out
}

func assistantToolMsg(m assistantMsg) map[string]any {
	calls := make([]map[string]any, 0, len(m.ToolCalls))
	for _, c := range m.ToolCalls {
		calls = append(calls, map[string]any{
			"id":   c.ID,
			"type": "function",
			"function": map[string]any{
				"name":      c.Function.Name,
				"arguments": c.Function.Arguments,
			},
		})
	}
	return map[string]any{"role": "assistant", "content": m.Content, "tool_calls": calls}
}

func capLog(s string) string {
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}

// telegramBotUploadLimit is the practical Bot API cap for sendDocument/sendVideo (~50 MiB).
// Larger files stay on disk and are reported by path (user runs locally).
const telegramBotUploadLimit = 49 << 20

// sendArtifacts delivers files the agent produced: PNG → photo, video → video/document,
// other files → local path note (user runs the bot on their machine).
func sendArtifacts(botAPI string, chatID int64, paths []string) {
	var leftovers []string
	for _, p := range paths {
		ext := strings.ToLower(filepath.Ext(p))
		switch {
		case ext == ".png" || ext == ".jpg" || ext == ".jpeg" || ext == ".webp":
			if err := sendTelegramPhotoFile(botAPI, chatID, p, ""); err != nil {
				log.Printf("[AGENT] send photo %s failed: %v", p, err)
				leftovers = append(leftovers, p)
			}
		case isVideoExt(ext):
			if err := sendTelegramVideoFile(botAPI, chatID, p, filepath.Base(p)); err != nil {
				log.Printf("[AGENT] send video %s failed: %v", p, err)
				leftovers = append(leftovers, p)
			}
		default:
			if fi, err := os.Stat(p); err == nil && fi.Size() <= telegramBotUploadLimit {
				if err := sendTelegramDocumentFile(botAPI, chatID, p, ""); err != nil {
					log.Printf("[AGENT] send document %s failed: %v", p, err)
					leftovers = append(leftovers, p)
				}
			} else {
				leftovers = append(leftovers, p)
			}
		}
	}
	if len(leftovers) > 0 {
		sendTelegramMessage(botAPI, chatID, "📁 Файлы сохранены локально:\n"+strings.Join(leftovers, "\n"))
	}
}

func isVideoExt(ext string) bool {
	switch strings.ToLower(ext) {
	case ".mp4", ".webm", ".mkv", ".mov", ".m4v", ".avi":
		return true
	}
	return false
}

// sendTelegramVideoFile uploads a local video (sendVideo if small enough, else sendDocument).
func sendTelegramVideoFile(botAPI string, chatID int64, path, caption string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if fi.Size() > telegramBotUploadLimit {
		return fmt.Errorf("файл %.1f МБ > лимита Telegram Bot API (~50 МБ)", float64(fi.Size())/(1<<20))
	}
	method := "sendDocument"
	field := "document"
	if strings.EqualFold(filepath.Ext(path), ".mp4") {
		method = "sendVideo"
		field = "video"
	}
	return sendTelegramMultipartFile(botAPI, method, field, chatID, path, caption)
}

// sendTelegramDocumentFile uploads any file as a Telegram document.
func sendTelegramDocumentFile(botAPI string, chatID int64, path, caption string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if fi.Size() > telegramBotUploadLimit {
		return fmt.Errorf("файл %.1f МБ > лимита Telegram Bot API (~50 МБ)", float64(fi.Size())/(1<<20))
	}
	return sendTelegramMultipartFile(botAPI, "sendDocument", "document", chatID, path, caption)
}

// sendTelegramMultipartFile is the shared multipart uploader for photo/video/document.
func sendTelegramMultipartFile(botAPI, method, field string, chatID int64, path, caption string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("chat_id", strconv.FormatInt(chatID, 10))
	if caption != "" {
		_ = mw.WriteField("caption", caption)
	}
	part, err := mw.CreateFormFile(field, filepath.Base(path))
	if err != nil {
		return err
	}
	if _, err := part.Write(data); err != nil {
		return err
	}
	mw.Close()
	resp, err := tgUploadHTTP.Post(botAPI+"/"+method, mw.FormDataContentType(), &body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != 200 {
		return fmt.Errorf("%s HTTP %d: %s", method, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var tg struct {
		Ok          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(respBody, &tg); err == nil && !tg.Ok {
		if tg.Description == "" {
			tg.Description = "ok=false"
		}
		return fmt.Errorf("%s: %s", method, tg.Description)
	}
	return nil
}

// sendTelegramPhotoFile uploads a local image to a chat via multipart sendPhoto.
func sendTelegramPhotoFile(botAPI string, chatID int64, path, caption string) error {
	return sendTelegramMultipartFile(botAPI, "sendPhoto", "photo", chatID, path, caption)
}
