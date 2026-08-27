package browser

import (
	"context"
	"fmt"
	"io"
	neturl "net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

// This file is the spec's tools.py: every browser capability is exposed as one OpenAI
// function tool, and Dispatch routes a model tool-call to the matching Session method.
// Results are returned as compact strings (usually JSON) that go straight back to the model.

// ===== Pack catalogs (ТЗ §5) =====
//
// The model never sees the whole catalog at once: the dispatcher picks packs and only those
// schemas are advertised. Descriptions are deliberately terse — TestPackSchemaBudget caps the
// worst-case union at 12 000 chars BEFORE the chat template's ~3× expansion.

// ToolsWeb is the `web` pack: search + read the open internet, no Chrome needed.
func (s *Session) ToolsWeb() []ToolSpec {
	return []ToolSpec{
		tool("web_search", "Поиск: Яндекс+Google (+Bing/DDG).", obj(props{
			"query": str(""),
			"limit": intp(""),
		}, "query")),
		tool("semantic_search", "Смысловой поиск.", obj(props{
			"query": str(""),
			"limit": intp(""),
		}, "query")),
		tool("read_url", "Страница как текст.", obj(props{
			"url": str(""),
		}, "url")),
		tool("http_get", "HTTP GET (API/JSON). Auth: authorization=Bearer …", obj(props{
			"url":           str(""),
			"authorization": str("Bearer …"),
		}, "url")),
		tool("github_search", "Поиск на GitHub.", obj(props{
			"query": str(""),
			"kind":  str("repos|code|issues|commits"),
			"limit": intp(""),
		}, "query")),
		tool("youtube_transcript", "Субтитры видео.", obj(props{
			"url":  str(""),
			"lang": str("ru,en"),
		}, "url")),
		tool("agent_reach_doctor", "Статус Agent Reach.", obj(props{
			"json": boolp(""),
		})),
		tool("agent_reach", "CLI agent-reach.", obj(props{
			"args": arrp(""),
		}, "args")),
	}
}

// ToolsBrowserRead is the `browser.read` pack: tabs, navigation and every read-only view of
// the live Chrome page (DOM, network, screenshot).
func (s *Session) ToolsBrowserRead() []ToolSpec {
	return []ToolSpec{
		tool("list_tabs", "Вкладки Chrome.", obj(nil)),
		tool("open_url", "Открыть URL.", obj(props{
			"url":     str(""),
			"new_tab": boolp(""),
		}, "url")),
		tool("switch_tab", "Сменить вкладку.", obj(props{
			"index": intp("из list_tabs"),
			"match": str("часть URL/заголовка"),
		})),
		tool("get_text", "Текст страницы.", obj(props{
			"selector": str("CSS"),
		})),
		tool("get_html", "outerHTML страницы.", obj(props{"selector": str("CSS")})),
		tool("analyze_dom", "Обзор DOM.", obj(nil)),
		tool("get_attributes", "Атрибуты элемента.", obj(props{"selector": str("")}, "selector")),
		tool("extract_links", "Ссылки.", obj(nil)),
		tool("extract_images", "Картинки.", obj(nil)),
		tool("extract_table", "Таблицы.", obj(props{
			"index":  intp(""),
			"format": str("json|csv|markdown"),
		})),
		tool("extract_forms", "Формы.", obj(nil)),
		tool("extract_json", "Встроенный JSON.", obj(props{"selector": str("CSS")})),
		tool("get_storage", "localStorage/sessionStorage.", obj(props{"type": str("local|session")}, "type")),
		tool("get_cookies", "Cookies.", obj(nil)),
		tool("get_network_requests", "Сетевые запросы.", obj(props{"filter": str("xhr|fetch|document")})),
		tool("get_websocket_messages", "Кадры WebSocket.", obj(nil)),
		tool("get_response_body", "Тело ответа.", obj(props{"request_id": str("")}, "request_id")),
		tool("download_file", "Скачать файл по URL.", obj(props{"url": str("")}, "url")),
		tool("clone_website", "Офлайн-клон страницы.", obj(props{"dir": str("")})),
		tool("capture_screenshot", "Скриншот (по просьбе).", obj(props{
			"selector":  str("CSS"),
			"full_page": boolp(""),
		})),
	}
}

// ToolsBrowserAct is the `browser.act` pack: everything that MUTATES the page. Each of these
// goes through the "did the tab change since the last read?" check in Dispatch.
func (s *Session) ToolsBrowserAct() []ToolSpec {
	return []ToolSpec{
		tool("click_element", "Клик по элементу: selector (CSS) ИЛИ text (надпись на кнопке/ссылке).", obj(props{
			"selector": str(""),
			"text":     str("надпись элемента"),
		})),
		tool("type_text", "Ввод текста в поле.", obj(props{
			"selector": str(""),
			"text":     str(""),
			"clear":    boolp("очистить поле"),
		}, "selector", "text")),
		tool("scroll_page", "Прокрутка.", obj(props{
			"selector": str("CSS"),
			"to":       str("bottom|top"),
			"amount":   intp("px"),
		})),
		tool("select_option", "Выбор в select.", obj(props{
			"selector": str(""),
			"value":    str("value/текст"),
		}, "selector", "value")),
		tool("submit_form", "Отправить форму.", obj(props{"selector": str("CSS")})),
		tool("upload_file", "Файлы в input[type=file].", obj(props{
			"selector": str(""),
			"paths":    arrp(""),
		}, "paths")),
		tool("drag_element", "Drag-and-drop.", obj(props{
			"from": str("CSS"),
			"to":   str("CSS"),
		}, "from", "to")),
		tool("close_page", "Закрыть вкладку.", obj(props{
			"index": intp("пусто = текущая"),
		})),
	}
}

// ToolsConsole is the `console` pack: the raw shell.
func (s *Session) ToolsConsole() []ToolSpec {
	return []ToolSpec{
		tool("run_command", "Команда в PowerShell (Windows) или bash. Обёртка powershell -Command не нужна.", obj(props{
			"command":     str(""),
			"cwd":         str("каталог"),
			"timeout_sec": intp("по умолч. 120"),
		}, "command")),
	}
}

// ToolsFiles is the `files` pack: local filesystem.
func (s *Session) ToolsFiles() []ToolSpec {
	return []ToolSpec{
		tool("read_file", "Чтение файла.", obj(props{
			"path":      str(""),
			"max_bytes": intp(""),
		}, "path")),
		tool("write_file", "Запись файла.", obj(props{
			"path":    str(""),
			"content": str(""),
			"append":  boolp("дописать"),
		}, "path", "content")),
		tool("list_dir", "Список файлов.", obj(props{
			"path": str("по умолч. ."),
		})),
		tool("file_info", "Метаданные пути.", obj(props{"path": str("")}, "path")),
		tool("mkdir", "Создать каталог.", obj(props{"path": str("")}, "path")),
		tool("delete_path", "Удалить файл/каталог.", obj(props{
			"path":      str(""),
			"recursive": boolp(""),
		}, "path")),
	}
}

// ToolsMedia is the `media` pack: video download + transcript (overlaps `web` on purpose;
// the pack assembler dedups by function name).
func (s *Session) ToolsMedia() []ToolSpec {
	return []ToolSpec{
		tool("download_video", "Скачать видео.", obj(props{
			"url": str(""),
		}, "url")),
		tool("youtube_transcript", "Субтитры видео.", obj(props{
			"url":  str(""),
			"lang": str("ru,en"),
		}, "url")),
	}
}

// ToolsCore is the compact legacy catalog (terminal + files + web + a few Chrome reads).
// The layered agent uses packs; this stays for direct callers and shrink fallbacks.
func (s *Session) ToolsCore() []ToolSpec {
	out := append([]ToolSpec{}, s.ToolsConsole()...)
	out = append(out, s.ToolsFiles()[:3]...) // read_file, write_file, list_dir
	out = append(out, s.ToolsWeb()[:6]...)   // search/read/http/github/transcript
	out = append(out, s.ToolsMedia()[0])     // download_video
	out = append(out, s.ToolsBrowserRead()[:2]...)
	out = append(out, s.ToolsBrowserRead()[3]) // get_text
	return DedupTools(out)
}

// Tools returns the whole catalog (every pack, deduped). Used by budget tests and any caller
// that genuinely wants everything; the agent loop prefers packs.
func (s *Session) Tools() []ToolSpec {
	out := append([]ToolSpec{}, s.ToolsConsole()...)
	out = append(out, s.ToolsFiles()...)
	out = append(out, s.ToolsWeb()...)
	out = append(out, s.ToolsMedia()...)
	out = append(out, s.ToolsBrowserRead()...)
	out = append(out, s.ToolsBrowserAct()...)
	return DedupTools(out)
}

// DedupTools keeps the first spec per function name (§5: overlapping packs are normal —
// youtube_transcript lives in both `web` and `media`).
func DedupTools(in []ToolSpec) []ToolSpec {
	seen := make(map[string]bool, len(in))
	out := make([]ToolSpec, 0, len(in))
	for _, t := range in {
		name := t.Function.Name
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, t)
	}
	return out
}

// browserActTools are the mutating page tools guarded by the tab-change check.
var browserActTools = map[string]bool{
	"click_element": true, "type_text": true, "scroll_page": true, "select_option": true,
	"submit_form": true, "upload_file": true, "drag_element": true,
}

// browserReadTools refresh the "which page did the model last look at" anchor.
var browserReadTools = map[string]bool{
	"get_text": true, "get_html": true, "analyze_dom": true, "get_attributes": true,
	"extract_links": true, "extract_images": true, "extract_table": true, "extract_forms": true,
	"extract_json": true, "capture_screenshot": true, "open_url": true, "switch_tab": true,
}

// Dispatch executes one tool call and returns a string result for the model. Tool-level
// failures are returned as the error so the agent can relay them as a `tool` message and let
// the model recover; only an unknown tool name is a hard error.
//
// ctx is the task context (§4.2): every child process and outbound HTTP request runs under
// it, so /stop and TaskTimeout reach powershell / yt-dlp / a stalled fetch, not just the loop.
func (s *Session) Dispatch(ctx context.Context, name string, args map[string]any) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	// §5: between get_text and click_element the page could have redirected, popped up a
	// dialog, or lost focus. Acting on a page the model never read is worse than refusing.
	if browserActTools[name] {
		if err := s.ensureSamePage(); err != nil {
			return "", err
		}
	}
	if browserReadTools[name] {
		defer s.noteCurrentPage()
	}
	switch name {
	// ---- computer ----
	case "run_command":
		return RunCommand(ctx, argStr(args, "command"), argStr(args, "cwd"), argInt(args, "timeout_sec", 0))
	case "read_file":
		return ReadLocalFile(argStr(args, "path"), argInt(args, "max_bytes", 0))
	case "write_file":
		return WriteLocalFile(argStr(args, "path"), argStr(args, "content"), argBool(args, "append"))
	case "list_dir":
		return ListLocalDir(argStr(args, "path"))
	case "file_info":
		return LocalFileInfo(argStr(args, "path"))
	case "mkdir":
		return MkdirLocal(argStr(args, "path"))
	case "delete_path":
		return DeleteLocal(argStr(args, "path"), argBool(args, "recursive"))

	// ---- reach ----
	case "web_search":
		results, err := s.WebSearch(ctx, argStr(args, "query"), argInt(args, "limit", 10))
		if err != nil {
			return "", err
		}
		if len(results) == 0 {
			return "Поиск не дал результатов.", nil
		}
		return formatSearchWithHarvest(ctx, s, results), nil
	case "semantic_search":
		return SemanticSearch(ctx, argStr(args, "query"), argInt(args, "limit", 5), s)
	case "read_url":
		return ReadURLChrome(ctx, argStr(args, "url"), s)
	case "http_get":
		return HTTPGet(ctx, argStr(args, "url"), argStr(args, "authorization"))
	case "youtube_transcript":
		return YouTubeTranscript(ctx, argStr(args, "url"), argStr(args, "lang"))
	case "github_search":
		return GitHubSearch(ctx, argStr(args, "kind"), argStr(args, "query"), argInt(args, "limit", 10))
	case "agent_reach_doctor":
		return AgentReachDoctor(ctx, argBool(args, "json"))
	case "agent_reach":
		return AgentReachRun(ctx, argStrSlice(args, "args"))

	case "list_tabs":
		tabs, err := s.ListTabs()
		if err != nil {
			return "", err
		}
		return toJSON(tabs), nil
	case "open_url":
		url, err := s.OpenURL(argStr(args, "url"), argBool(args, "new_tab"))
		if err != nil {
			return "", err
		}
		return "Открыто: " + url, nil
	case "switch_tab":
		tab, err := s.SwitchTab(argInt(args, "index", 0), argStr(args, "match"))
		if err != nil {
			return "", err
		}
		return "Активная вкладка: " + tab.URL, nil
	case "close_page":
		if err := s.ClosePage(argInt(args, "index", -1)); err != nil {
			return "", err
		}
		return "Вкладка закрыта.", nil

	case "click_element":
		// Текст надёжнее селектора на живых SPA: разметка там генерируется, а надпись на
		// кнопке — то же самое, что видит человек.
		if txt := argStr(args, "text"); txt != "" {
			if err := s.ClickText(txt); err != nil {
				return "", err
			}
			return "Клик по элементу с текстом «" + txt + "» выполнен.", nil
		}
		if err := s.Click(argStr(args, "selector")); err != nil {
			return "", err
		}
		return "Клик выполнен.", nil
	case "type_text":
		if err := s.TypeText(argStr(args, "selector"), argStr(args, "text"), argBool(args, "clear")); err != nil {
			return "", err
		}
		return "Текст введён.", nil
	case "scroll_page":
		if err := s.Scroll(argStr(args, "selector"), argStr(args, "to"), argInt(args, "amount", 0)); err != nil {
			return "", err
		}
		return "Прокручено.", nil
	case "select_option":
		if err := s.SelectOption(argStr(args, "selector"), argStr(args, "value")); err != nil {
			return "", err
		}
		return "Опция выбрана.", nil
	case "submit_form":
		if err := s.SubmitForm(argStr(args, "selector")); err != nil {
			return "", err
		}
		return "Форма отправлена.", nil
	case "upload_file":
		if err := s.UploadFile(argStr(args, "selector"), argStrSlice(args, "paths")); err != nil {
			return "", err
		}
		return "Файлы прикреплены.", nil
	case "drag_element":
		if err := s.DragElement(argStr(args, "from"), argStr(args, "to")); err != nil {
			return "", err
		}
		return "Перетаскивание выполнено.", nil

	case "analyze_dom":
		sum, err := s.AnalyzeDOM()
		if err != nil {
			return "", err
		}
		return toJSON(sum), nil
	case "get_html":
		html, err := s.GetHTML(argStr(args, "selector"))
		if err != nil {
			return "", err
		}
		return capStr(html, 8000), nil
	case "get_text":
		txt, err := s.GetText(argStr(args, "selector"))
		if err != nil {
			return "", err
		}
		return capStr(txt, 8000), nil
	case "get_attributes":
		attrs, err := s.GetAttributes(argStr(args, "selector"))
		if err != nil {
			return "", err
		}
		return toJSON(attrs), nil
	case "extract_links":
		links, err := s.ExtractLinks()
		if err != nil {
			return "", err
		}
		return capList(links, 200), nil
	case "extract_images":
		imgs, err := s.ExtractImages()
		if err != nil {
			return "", err
		}
		return capList(imgs, 200), nil
	case "extract_table":
		return s.dispatchExtractTable(args)
	case "extract_forms":
		forms, err := s.ExtractForms()
		if err != nil {
			return "", err
		}
		return toJSON(forms), nil
	case "extract_json":
		items, err := s.ExtractJSON(argStr(args, "selector"))
		if err != nil {
			return "", err
		}
		if len(items) == 0 {
			return "Встроенный JSON не найден.", nil
		}
		return capStr(toJSON(items), 8000), nil
	case "get_storage":
		st, err := s.GetStorage(argStr(args, "type"))
		if err != nil {
			return "", err
		}
		return toJSON(st), nil
	case "get_cookies":
		ck, err := s.GetCookies()
		if err != nil {
			return "", err
		}
		return toJSON(ck), nil

	case "get_network_requests":
		if fresh, err := s.EnableNetworkCapture(); err != nil {
			return "", err
		} else if fresh {
			return networkJustEnabledNote, nil
		}
		reqs := s.Requests(argStr(args, "filter"))
		return capList(reqs, 150), nil
	case "get_websocket_messages":
		if fresh, err := s.EnableNetworkCapture(); err != nil {
			return "", err
		} else if fresh {
			return networkJustEnabledNote, nil
		}
		return capList(s.WebSocketMessages(), 150), nil
	case "get_response_body":
		if _, err := s.EnableNetworkCapture(); err != nil {
			return "", err
		}
		body, err := s.GetResponseBody(argStr(args, "request_id"))
		if err != nil {
			return "", err
		}
		return capStr(body, 8000), nil

	case "download_file":
		p, err := s.DownloadFile(argStr(args, "url"))
		if err != nil {
			return "", err
		}
		return "Скачано в " + p, nil
	case "download_video":
		res, err := s.DownloadVideo(ctx, argStr(args, "url"), s.FFmpegPath)
		if err != nil {
			return "", err
		}
		return FormatVideoResult(res), nil
	case "clone_website":
		res, err := s.CloneWebsite(argStr(args, "dir"))
		if err != nil {
			return "", err
		}
		return toJSON(res), nil
	case "capture_screenshot":
		p, err := s.CaptureScreenshot(argStr(args, "selector"), argBool(args, "full_page"))
		if err != nil {
			return "", err
		}
		return "Скриншот сохранён: " + p, nil
	}
	return "", fmt.Errorf("неизвестный инструмент: %s", name)
}

// dispatchExtractTable handles the two modes of extract_table (list vs one table).
func (s *Session) dispatchExtractTable(args map[string]any) (string, error) {
	tables, err := s.ExtractTables()
	if err != nil {
		return "", err
	}
	if len(tables) == 0 {
		return "Таблиц на странице не найдено.", nil
	}
	idx := argInt(args, "index", -1)
	if idx < 0 {
		// List mode: let the model see what's available and pick one.
		type summary struct {
			Index   int      `json:"index"`
			Headers []string `json:"headers"`
			Rows    int      `json:"rows"`
		}
		var out []summary
		for i, t := range tables {
			out = append(out, summary{Index: i, Headers: t.Headers, Rows: len(t.Rows)})
		}
		return toJSON(out), nil
	}
	if idx >= len(tables) {
		return "", fmt.Errorf("индекс таблицы %d вне диапазона 0..%d", idx, len(tables)-1)
	}
	content, _ := exportTable(tables[idx], argStr(args, "format"))
	return capStr(content, 8000), nil
}

// DownloadFile fetches a direct URL to disk under runtime/browser. Relative URLs resolve
// against the current page. Returns the saved path.
func (s *Session) DownloadFile(rawURL string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", fmt.Errorf("нужен URL файла")
	}
	if !strings.Contains(rawURL, "://") {
		if cur, err := s.CurrentURL(); err == nil {
			rawURL = resolveURL(cur, rawURL)
		}
	}
	// Block SSRF: only public http(s) targets (no file://, loopback, or metadata IPs).
	safe, err := safeRemoteURL(rawURL)
	if err != nil {
		return "", err
	}
	rawURL = safe
	resp, err := publicHTTP.Get(rawURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("HTTP %d при скачивании", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAssetBytes))
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		return "", err
	}
	// derive the name from the parsed URL path — path.Base on the raw URL would glue the
	// query string into the name (report.pdf?sid=abc → report.pdf_sid_abc, extension lost)
	name := ""
	if u, perr := neturl.Parse(rawURL); perr == nil {
		name = sanitizeName(path.Base(u.Path))
	}
	if name == "" || name == "." || name == "/" || name == "_" {
		name = "download.bin"
	}
	full := filepath.Join(artifactDir, name)
	if err := os.WriteFile(full, data, 0o644); err != nil {
		return "", err
	}
	s.addArtifact(full)
	return full, nil
}

// ===== small helpers for tool specs & arg parsing =====

type props = map[string]any

func tool(name, desc string, params map[string]any) ToolSpec {
	return ToolSpec{Type: "function", Function: ToolFunction{Name: name, Description: desc, Parameters: params}}
}

// obj builds a JSON-Schema object. nil properties → a no-arg tool.
func obj(p props, required ...string) map[string]any {
	if p == nil {
		p = props{}
	}
	m := map[string]any{"type": "object", "properties": p}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}

// str/boolp/intp/arrp build one parameter. An EMPTY description is omitted from the schema on
// purpose: self-evident names (url, path, selector) do not need prose, and every dropped
// description is ~35 bytes off the pack budget, which is checked by TestPackSchemaBudget at
// 12 000 chars for the worst-case union of all packs (§5).
func str(desc string) map[string]any   { return param("string", desc) }
func boolp(desc string) map[string]any { return param("boolean", desc) }
func intp(desc string) map[string]any  { return param("integer", desc) }

func param(kind, desc string) map[string]any {
	m := map[string]any{"type": kind}
	if desc != "" {
		m["description"] = desc
	}
	return m
}

func arrp(desc string) map[string]any {
	m := map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	if desc != "" {
		m["description"] = desc
	}
	return m
}

func argStr(a map[string]any, k string) string {
	if v, ok := a[k]; ok {
		if s, ok := v.(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func argBool(a map[string]any, k string) bool {
	if v, ok := a[k]; ok {
		switch b := v.(type) {
		case bool:
			return b
		case string:
			return b == "true" || b == "1"
		}
	}
	return false
}

func argInt(a map[string]any, k string, def int) int {
	if v, ok := a[k]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		case string:
			if x, err := strconv.Atoi(strings.TrimSpace(n)); err == nil {
				return x
			}
		}
	}
	return def
}

func argStrSlice(a map[string]any, k string) []string {
	out := []string{}
	if v, ok := a[k]; ok {
		switch arr := v.(type) {
		case []any:
			for _, e := range arr {
				if s, ok := e.(string); ok {
					out = append(out, s)
				}
			}
		case []string:
			out = append(out, arr...)
		case string:
			if strings.TrimSpace(arr) != "" {
				out = append(out, arr)
			}
		}
	}
	return out
}

// capStr truncates long tool output so it doesn't blow the model's context window.
func capStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// back off to a rune boundary — byte-slicing Cyrillic (2 bytes/char) in half
	// produces an invalid-UTF-8 tail
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + fmt.Sprintf("\n…(обрезано, всего %d символов)", len(s))
}

// capList JSON-encodes a slice, keeping at most n elements and noting how many were dropped.
func capList[T any](items []T, n int) string {
	if len(items) <= n {
		return toJSON(items)
	}
	out := toJSON(items[:n])
	return out + fmt.Sprintf("\n…(показаны первые %d из %d)", n, len(items))
}
