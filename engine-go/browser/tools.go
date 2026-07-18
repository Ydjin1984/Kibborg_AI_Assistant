package browser

import (
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

// Tools returns the full tool catalog advertised to the model.
func (s *Session) Tools() []ToolSpec {
	return []ToolSpec{
		// ---- web search ----
		tool("web_search", "Поиск в интернете (DuckDuckGo): вернёт список результатов (заголовок, URL, сниппет). Дальше открой нужную ссылку через open_url. Не требует запущенного Chrome.", obj(props{
			"query": str("Поисковый запрос"),
			"limit": intp("Сколько результатов вернуть (по умолчанию 10, максимум 25)"),
		}, "query")),

		// ---- tabs / navigation ----
		tool("list_tabs", "Список уже открытых вкладок Chrome (индекс, заголовок, URL). Начинай с этого, чтобы понять, с чем работаешь.", obj(nil)),
		tool("open_url", "Открыть URL в текущей вкладке (или в новой, если new_tab=true).", obj(props{
			"url":     str("Адрес страницы"),
			"new_tab": boolp("Открыть в новой вкладке вместо текущей"),
		}, "url")),
		tool("switch_tab", "Переключиться на другую открытую вкладку по индексу или по подстроке URL/заголовка.", obj(props{
			"index": intp("0-based индекс вкладки из list_tabs"),
			"match": str("Подстрока URL или заголовка"),
		})),
		tool("close_page", "Закрыть вкладку по индексу (или текущую, если индекс не задан).", obj(props{
			"index": intp("Индекс вкладки из list_tabs; не задан = текущая"),
		})),

		// ---- actions ----
		tool("click_element", "Кликнуть по элементу (CSS-селектор).", obj(props{"selector": str("CSS-селектор")}, "selector")),
		tool("type_text", "Ввести текст в поле (CSS-селектор). clear=true очищает поле перед вводом.", obj(props{
			"selector": str("CSS-селектор поля ввода"),
			"text":     str("Текст для ввода"),
			"clear":    boolp("Очистить поле перед вводом"),
		}, "selector", "text")),
		tool("scroll_page", "Прокрутить страницу: amount=пиксели (вниз), либо to='bottom'/'top'. С selector — прокрутить к элементу.", obj(props{
			"selector": str("CSS-селектор элемента, к которому прокрутить (необязательно)"),
			"to":       str("'bottom' или 'top'"),
			"amount":   intp("Пиксели прокрутки вниз (по умолчанию 600)"),
		})),
		tool("select_option", "Выбрать значение в <select> по value или видимому тексту опции.", obj(props{
			"selector": str("CSS-селектор <select>"),
			"value":    str("value или текст опции"),
		}, "selector", "value")),
		tool("submit_form", "Отправить форму (CSS-селектор формы или её элемента; по умолчанию первая form).", obj(props{"selector": str("CSS-селектор формы")})),
		tool("upload_file", "Загрузить локальные файлы в input[type=file].", obj(props{
			"selector": str("CSS-селектор file-input (по умолчанию input[type=file])"),
			"paths":    arrp("Локальные пути к файлам"),
		}, "paths")),
		tool("drag_element", "Перетащить элемент (источник → цель), best-effort HTML5 drag-and-drop.", obj(props{
			"from": str("CSS-селектор источника"),
			"to":   str("CSS-селектор цели"),
		}, "from", "to")),

		// ---- DOM / data ----
		tool("analyze_dom", "Структурный обзор текущей страницы: заголовок, заголовки h1-h3, счётчики ссылок/картинок/форм/таблиц. Хорошая отправная точка.", obj(nil)),
		tool("get_html", "Получить outerHTML элемента (или всей страницы, если selector пуст).", obj(props{"selector": str("CSS-селектор (необязательно)")})),
		tool("get_text", "Получить видимый текст элемента (или body).", obj(props{"selector": str("CSS-селектор (необязательно)")})),
		tool("get_attributes", "Получить атрибуты первого элемента по селектору.", obj(props{"selector": str("CSS-селектор")}, "selector")),
		tool("extract_links", "Собрать все ссылки страницы (текст + абсолютный href).", obj(nil)),
		tool("extract_images", "Собрать все изображения страницы (абсолютный src + alt).", obj(nil)),
		tool("extract_table", "Извлечь таблицы. Без index — список таблиц (заголовки, число строк). С index+format — таблица в формате json/records/csv/markdown.", obj(props{
			"index":  intp("Индекс таблицы (из списка)"),
			"format": str("json | records | csv | markdown"),
		})),
		tool("extract_forms", "Описать формы страницы (action, method, поля) — чтобы найти форму входа/поиска.", obj(nil)),
		tool("extract_json", "Извлечь встроенный JSON: <script type=application/json>, JSON-LD или по своему селектору.", obj(props{"selector": str("CSS-селектор источника JSON (необязательно)")})),
		tool("get_storage", "Прочитать localStorage или sessionStorage. type='local'|'session'.", obj(props{"type": str("'local' или 'session'")}, "type")),
		tool("get_cookies", "Прочитать cookies текущей страницы.", obj(nil)),

		// ---- network ----
		tool("get_network_requests", "Захваченные сетевые запросы (URL, метод, тип, статус). filter — по типу ресурса, напр. 'xhr', 'fetch', 'document'.", obj(props{"filter": str("Подстрока типа ресурса: xhr/fetch/document/script…")})),
		tool("get_websocket_messages", "Захваченные кадры WebSocket (направление, payload).", obj(nil)),
		tool("get_response_body", "Тело ответа по request_id (взять из get_network_requests). Работает, пока ответ ещё в буфере.", obj(props{"request_id": str("RequestID из get_network_requests")}, "request_id")),

		// ---- files / clone / screenshot / video ----
		tool("download_file", "Скачать файл по прямому URL на диск (runtime/browser). Относительный URL резолвится к текущей странице.", obj(props{"url": str("Прямой URL файла")}, "url")),
		tool("download_video", "Скачать видео с YouTube / Instagram / TikTok / X и т.п. в максимальном доступном качестве (yt-dlp). Не требует Chrome. Вернёт путь к файлу.", obj(props{
			"url": str("Ссылка на видео (youtube.com, youtu.be, instagram.com/reel/…, tiktok, …)"),
		}, "url")),
		tool("clone_website", "Клонировать текущую страницу офлайн (HTML/CSS/JS/img/шрифты/svg) с перелинковкой в локальные пути.", obj(props{"dir": str("Каталог назначения (необязательно)")})),
		tool("capture_screenshot", "СКРИНШОТ — только если пользователь явно просит визуальный анализ. selector — элемент, full_page — вся страница.", obj(props{
			"selector":  str("CSS-селектор элемента (необязательно)"),
			"full_page": boolp("Снять всю прокручиваемую страницу"),
		})),
	}
}

// Dispatch executes one tool call and returns a string result for the model. Tool-level
// failures are returned as the error so the agent can relay them as a `tool` message and let
// the model recover; only an unknown tool name is a hard error.
func (s *Session) Dispatch(name string, args map[string]any) (string, error) {
	switch name {
	case "web_search":
		results, err := WebSearch(argStr(args, "query"), argInt(args, "limit", 10))
		if err != nil {
			return "", err
		}
		if len(results) == 0 {
			return "Поиск не дал результатов.", nil
		}
		return toJSON(results), nil
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
		reqs := s.Requests(argStr(args, "filter"))
		return capList(reqs, 150), nil
	case "get_websocket_messages":
		return capList(s.WebSocketMessages(), 150), nil
	case "get_response_body":
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
		res, err := s.DownloadVideo(argStr(args, "url"), s.FFmpegPath)
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
	resp, err := cloneHTTP.Get(rawURL)
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

func str(desc string) map[string]any   { return map[string]any{"type": "string", "description": desc} }
func boolp(desc string) map[string]any { return map[string]any{"type": "boolean", "description": desc} }
func intp(desc string) map[string]any  { return map[string]any{"type": "integer", "description": desc} }
func arrp(desc string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
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
