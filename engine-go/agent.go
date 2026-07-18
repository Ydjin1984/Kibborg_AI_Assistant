package main

// Browser agent: the tool-calling loop that lets the local LLM drive a real Chrome through
// the browser package. The model selects tools (open_url, extract_table, …), we execute them
// against the running browser, feed the results back, and repeat until it produces a final
// answer. This is the "Работа по естественному языку" + "Tool Calling" part of the spec.

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

	"kibborg/engine/browser"
)

// browserSystemPrompt advertises the agent's full web arsenal and enforces the spec's rules:
// DOM/network first, screenshots only on explicit request, never invent data, answer in Russian.
const browserSystemPrompt = `Ты — Browser Agent внутри Kibborg: у тебя ПОЛНОЦЕННЫЙ веб-арсенал. Ты не просто читаешь страницу — ты ищешь в интернете, ходишь по сайтам, тянешь данные из DOM и сети, автоматизируешь действия, скачиваешь и сохраняешь результат. Управляешь уже открытым Google Chrome через Chrome DevTools Protocol. Отвечай по-русски.

ТВОЙ ИНСТРУМЕНТАРИЙ (выбирай сам, что нужно под задачу):
- 🔎 Поиск в интернете: web_search — найти страницы по запросу (заголовок, URL, сниппет). Работает даже без открытого Chrome. Дальше открывай нужную ссылку через open_url.
- 🧭 Навигация и вкладки: list_tabs, open_url, switch_tab, close_page.
- 🧩 Данные из DOM: analyze_dom, get_text, get_html, get_attributes, extract_links, extract_images, extract_table, extract_forms, extract_json.
- 🌐 Сеть напрямую: get_network_requests (XHR/Fetch/Document…), get_websocket_messages, get_response_body — часто данные удобнее взять из API-ответов сайта, чем парсить HTML.
- 🔐 Состояние: get_cookies, get_storage (local/session).
- 🖱 Действия: click_element, type_text, select_option, submit_form, scroll_page, upload_file, drag_element — заполнить форму, авторизоваться, нажать кнопки.
- 💾 Сохранение: download_file, download_video (YouTube/Instagram/TikTok… в макс. качестве), clone_website, capture_screenshot.
- 📤 Экспорт таблиц/данных: json / records / csv / markdown (через extract_table).

КАК РАБОТАТЬ:
- Нужного сайта нет в открытых вкладках? Сначала web_search, затем open_url по лучшей ссылке (или open_url сразу, если знаешь адрес).
- Сориентируйся (list_tabs / analyze_dom), потом тяни данные из DOM и сети — это основной источник.
- Ссылка на YouTube / Instagram / TikTok / X и просят скачать видео → сразу download_video (не open_url и не download_file).
- Скриншоты (capture_screenshot) — ТОЛЬКО если пользователь явно просит визуальный анализ (график, картинка, «как выглядит страница»). Иначе не делай.
- Новый браузер не запускай, работай с уже открытым Chrome. Для действий — точные CSS-селекторы.
- Никогда не выдумывай данные: сообщай только то, что реально вернули инструменты. Нет данных — так и скажи.
- Действуй минимальным числом шагов. Когда задача выполнена — дай краткий понятный итог по-русски.`

const maxAgentSteps = 8

var (
	browserMu      sync.Mutex
	browserSession *browser.Session
)

// browserTaskMu serializes whole browser tasks. The Chrome session is process-wide (one
// attached tab, one shared artifact buffer), so two concurrent /browser requests from
// different chats would fight over the tab and cross-deliver each other's screenshots.
// One task at a time keeps the session coherent; other chats' non-browser work is unaffected.
var browserTaskMu sync.Mutex

// getBrowserSession returns the process-wide browser session (lazily created). It attaches to
// Chrome on first tool use, not here. ffmpegPath is applied when the session is first created
// (and refreshed if already live) so download_video can mux best video+audio.
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

// closeBrowserSession detaches from Chrome (used by graceful shutdown). The user's tabs
// stay open — Session.Close only releases the DevTools attachment.
func closeBrowserSession() {
	browserMu.Lock()
	defer browserMu.Unlock()
	if browserSession != nil {
		browserSession.Close()
		browserSession = nil
	}
}

// runBrowserAgent executes one natural-language browser task and returns the final text plus
// any artifacts (screenshots/clones/downloads/videos) produced along the way.
func runBrowserAgent(cfg Config, task string) (string, []string) {
	browserTaskMu.Lock()
	defer browserTaskMu.Unlock()
	sess := getBrowserSessionWithFFmpeg(cfg.FfmpegPath)
	msgs := []map[string]any{
		{"role": "system", "content": browserSystemPrompt},
		{"role": "user", "content": task},
	}
	tools := sess.Tools()

	var lastText string
	for step := 0; step < maxAgentSteps; step++ {
		msg, err := llmChatTools(cfg.BrainPort, msgs, tools, 0.3)
		if err != nil {
			log.Printf("[BROWSER] llm error: %v", err)
			return "❌ Ошибка модели: " + err.Error(), sess.TakeArtifacts()
		}
		if len(msg.ToolCalls) == 0 {
			lastText = msg.Content
			break
		}
		msgs = append(msgs, assistantToolMsg(msg))
		for _, tc := range msg.ToolCalls {
			result := executeToolCall(sess, tc)
			msgs = append(msgs, map[string]any{
				"role":         "tool",
				"tool_call_id": tc.ID,
				"name":         tc.Function.Name,
				"content":      result,
			})
		}
	}

	// If we exhausted the step budget (or got no prose), force a no-tools summary so the user
	// always gets a readable answer instead of silence.
	if strings.TrimSpace(lastText) == "" {
		msgs = append(msgs, map[string]any{"role": "user", "content": "Подведи краткий итог по-русски на основе собранных данных."})
		if summary, err := llmChatTools(cfg.BrainPort, msgs, nil, 0.3); err == nil {
			lastText = summary.Content
		}
	}
	if strings.TrimSpace(lastText) == "" {
		lastText = "Готово, но модель не вернула текстовый итог. Повтори запрос конкретнее."
	}
	return lastText, sess.TakeArtifacts()
}

// assistantToolMsg rebuilds the assistant turn (with its tool_calls) as a generic map so the
// follow-up role:"tool" results line up with their call ids.
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

// executeToolCall parses the model's arguments and runs the tool, returning a string the
// model will read. Failures come back as text (not Go errors) so the loop can recover.
func executeToolCall(sess *browser.Session, tc toolCall) string {
	args := map[string]any{}
	raw := strings.TrimSpace(tc.Function.Arguments)
	if raw != "" && raw != "null" {
		if err := json.Unmarshal([]byte(raw), &args); err != nil {
			return "Ошибка разбора аргументов JSON: " + err.Error()
		}
	}
	log.Printf("[BROWSER] tool %s(%s)", tc.Function.Name, capLog(raw))
	res, err := sess.Dispatch(tc.Function.Name, args)
	if err != nil {
		return "Ошибка инструмента " + tc.Function.Name + ": " + err.Error()
	}
	return res
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
				log.Printf("[BROWSER] send photo %s failed: %v", p, err)
				leftovers = append(leftovers, p)
			}
		case isVideoExt(ext):
			if err := sendTelegramVideoFile(botAPI, chatID, p, filepath.Base(p)); err != nil {
				log.Printf("[BROWSER] send video %s failed: %v", p, err)
				leftovers = append(leftovers, p)
			}
		default:
			// Try as document if small enough; else just report the path.
			if fi, err := os.Stat(p); err == nil && fi.Size() <= telegramBotUploadLimit {
				if err := sendTelegramDocumentFile(botAPI, chatID, p, ""); err != nil {
					log.Printf("[BROWSER] send document %s failed: %v", p, err)
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
	// Prefer sendVideo for mp4; fall back to document for other containers.
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
// Telegram often returns HTTP 200 with {"ok":false,"description":…} — we parse the body.
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
