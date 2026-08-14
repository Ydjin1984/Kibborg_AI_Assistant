package main

// Живые статусы шага: не «читаю / ищу», а что именно читается и что ищется.
// Одна строка уходит и в веб-ленту, и в Telegram (паритет каналов).

import (
	"encoding/json"
	"net/url"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

func (ls *loopState) rememberAct(s string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return
	}
	ls.seenActs = append(ls.seenActs, s)
	if len(ls.seenActs) > 5 {
		ls.seenActs = ls.seenActs[len(ls.seenActs)-5:]
	}
}

func (ls *loopState) thinkLine() string {
	if len(ls.seenActs) == 0 {
		q := oneLine(ls.task.Input)
		if q != "" {
			return "🧠 Обдумываю запрос: «" + clipStatus(q, 100) + "»"
		}
		return "🧠 Обдумываю…"
	}
	return "🧠 Обдумываю собранное: " + strings.Join(ls.seenActs, " · ")
}

// toolStatusWord — короткая подпись без аргументов (фолбэк и тесты).
func toolStatusWord(name string) string {
	return toolStatusLine(name, nil)
}

func toolStatusLine(name string, args map[string]any) string {
	q := firstArg(args, "query", "q", "find")
	u := firstArg(args, "url", "href", "link")
	p := firstArg(args, "path", "file", "input")
	host := shortURL(u)
	file := shortPath(p)
	text := firstArg(args, "text", "look")
	sym := firstArg(args, "symbol", "ticker")
	cmd := firstArg(args, "command", "cmd")

	switch name {
	case "web_search", "semantic_search":
		if q != "" {
			return "🔎 Ищу: «" + clipStatus(q, 90) + "»"
		}
		return "🔎 Ищу…"
	case "github_search":
		if q != "" {
			return "🔎 Ищу на GitHub: «" + clipStatus(q, 80) + "»"
		}
		return "🔎 Ищу на GitHub…"
	case "read_url", "http_get":
		if host != "" {
			return "📖 Читаю " + host
		}
		return "📖 Читаю страницу…"
	case "open_url":
		if host != "" {
			return "🌐 Открываю " + host
		}
		return "🌐 Открываю страницу…"
	case "get_text", "get_html", "analyze_dom":
		if host != "" {
			return "📖 Читаю вкладку " + host
		}
		return "📖 Читаю вкладку…"
	case "youtube_transcript":
		if host != "" {
			return "🎬 Беру субтитры " + host
		}
		return "🎬 Беру субтитры…"
	case "run_command":
		if cmd != "" {
			return "🖥 Команда: " + clipStatus(oneLine(cmd), 90)
		}
		return "🖥 Выполняю команду…"
	case "read_file":
		if file != "" {
			return "📂 Читаю файл «" + file + "»"
		}
		return "📂 Читаю файл…"
	case "write_file":
		if file != "" {
			return "📁 Пишу в «" + file + "»"
		}
		return "📁 Пишу файл…"
	case "list_dir":
		if p != "" {
			return "📂 Смотрю папку «" + clipStatus(p, 70) + "»"
		}
		return "📂 Смотрю папку…"
	case "mkdir":
		if p != "" {
			return "📁 Создаю папку «" + clipStatus(p, 70) + "»"
		}
		return "📁 Создаю папку…"
	case "delete_path":
		if file != "" {
			return "🗑 Удаляю «" + file + "»"
		}
		return "🗑 Удаляю…"
	case "read_document":
		if file != "" {
			if q != "" {
				return "📄 Ищу в «" + file + "»: «" + clipStatus(q, 50) + "»"
			}
			return "📄 Читаю документ «" + file + "»"
		}
		return "📄 Читаю документ…"
	case "download_video", "download_file":
		if host != "" {
			return "⬇️ Скачиваю " + host
		}
		return "⬇️ Скачиваю…"
	case "analyze_video", "transcribe_media":
		if file != "" {
			return "🎬 Разбираю видео «" + file + "»"
		}
		return "🎬 Разбираю видео…"
	case "video_frames":
		if file != "" {
			return "👁 Смотрю кадры «" + file + "»"
		}
		return "👁 Смотрю кадры…"
	case "media_info":
		if file != "" {
			return "🎬 Метаданные «" + file + "»"
		}
		return "🎬 Смотрю метаданные…"
	case "convert_media":
		if file != "" {
			return "🎞 Конвертирую «" + file + "»"
		}
		return "🎞 Конвертирую…"
	case "speak_text":
		if text != "" {
			return "🔊 Озвучиваю: «" + clipStatus(text, 70) + "»"
		}
		return "🔊 Озвучиваю…"
	case "analyze_ticker":
		if sym != "" {
			return "📊 Считаю " + strings.ToUpper(sym)
		}
		return "📊 Считаю по рынку…"
	case "size_position":
		if sym != "" {
			return "📊 Считаю размер по " + strings.ToUpper(sym)
		}
		return "📊 Считаю размер позиции…"
	case "journal_add":
		if sym != "" {
			return "📒 Пишу в журнал " + strings.ToUpper(sym)
		}
		return "📒 Пишу в журнал…"
	case "journal_stats":
		return "📒 Смотрю журнал сделок…"
	case "analyze_log", "scan_text", "audit_file":
		if file != "" {
			return "🛡 Разбираю «" + file + "»"
		}
		return "🛡 Разбираю на безопасность…"
	case "click_element":
		if text != "" {
			return "🖱 Нажимаю «" + clipStatus(text, 50) + "»"
		}
		return "🖱 Нажимаю в браузере…"
	case "type_text":
		if text != "" {
			return "⌨️ Ввожу «" + clipStatus(text, 50) + "»"
		}
		return "⌨️ Ввожу текст…"
	case "submit_form", "select_option", "upload_file", "drag_element":
		return "🖱 Действую в браузере…"
	case "capture_screen":
		if text != "" {
			return "📸 Снимаю экран: «" + clipStatus(text, 60) + "»"
		}
		return "📸 Снимаю экран…"
	case "capture_screenshot":
		return "📸 Снимаю вкладку…"
	case "list_windows", "focus_window":
		return "🪟 Смотрю окна…"
	case "type_keyboard", "press_keys":
		if text != "" {
			return "⌨️ Печатаю «" + clipStatus(text, 40) + "»"
		}
		return "⌨️ Печатаю…"
	case "mouse_action":
		return "🖱 Веду мышь…"
	case "list_processes":
		return "⚙️ Смотрю процессы…"
	case "kill_process":
		return "⚙️ Останавливаю процесс…"
	case "launch_app":
		if app := firstArg(args, "app", "name", "path"); app != "" {
			return "🚀 Запускаю «" + clipStatus(filepath.Base(app), 50) + "»"
		}
		return "🚀 Запускаю программу…"
	case "clipboard":
		return "📋 Работаю с буфером обмена…"
	case "agent_reach_doctor":
		return "⚙️ Проверяю Agent Reach…"
	case "agent_reach":
		return "⚙️ Запускаю Agent Reach…"
	default:
		return "⚙️ " + name + "…"
	}
}

func toolActHint(name string, args map[string]any) string {
	q := firstArg(args, "query", "q", "find")
	if q != "" {
		return "поиск «" + clipStatus(q, 50) + "»"
	}
	if u := shortURL(firstArg(args, "url", "href", "link")); u != "" {
		return u
	}
	if p := shortPath(firstArg(args, "path", "file", "input")); p != "" {
		return "«" + p + "»"
	}
	if s := firstArg(args, "symbol", "ticker"); s != "" {
		return strings.ToUpper(s)
	}
	if c := firstArg(args, "command", "cmd"); c != "" {
		return "команда " + clipStatus(oneLine(c), 40)
	}
	_ = name
	return ""
}

func toolResultLine(name string, args map[string]any, res ToolResult) string {
	switch res.Status {
	case StatusBlocked, StatusDenied, StatusDeferred, StatusCancelled:
		return ""
	case StatusFailed, StatusTimeout:
		why := oneLine(res.Text)
		if why == "" && res.Err != nil {
			why = res.Err.Error()
		}
		if why == "" {
			why = string(res.Status)
		}
		return "✗ Не вышло: " + clipStatus(why, 110)
	}
	body := res.Text
	switch name {
	case "web_search", "semantic_search", "github_search":
		n, titles := searchHitHint(body)
		if n == 0 {
			return "✗ Поиск ничего не дал"
		}
		line := "✓ Нашёл " + ruCount(n, "ссылку", "ссылки", "ссылок")
		if titles != "" {
			line += ": " + titles
		}
		return clipStatus(line, 160)
	case "read_url", "http_get", "open_url", "get_text":
		host := shortURL(firstArg(args, "url", "href", "link"))
		title := pageTitleHint(body)
		n := utf8.RuneCountInString(body)
		line := "✓ Прочитал"
		if host != "" {
			line += " " + host
		}
		if title != "" {
			line += " — «" + title + "»"
		}
		if n > 400 {
			line += " · " + ruCount(n, "знак", "знака", "знаков")
		}
		return clipStatus(line, 160)
	case "read_file", "read_document":
		file := shortPath(firstArg(args, "path", "file"))
		n := utf8.RuneCountInString(body)
		line := "✓ Прочитал"
		if file != "" {
			line += " «" + file + "»"
		}
		if n > 80 {
			line += " · " + ruCount(n, "знак", "знака", "знаков")
		}
		return clipStatus(line, 140)
	case "analyze_ticker":
		if s := firstArg(args, "symbol", "ticker"); s != "" {
			return "✓ Посчитал " + strings.ToUpper(s)
		}
		return "✓ Посчитал тикер"
	case "youtube_transcript":
		return "✓ Субтитры получены · " + ruCount(utf8.RuneCountInString(body), "знак", "знака", "знаков")
	case "run_command":
		if strings.TrimSpace(body) == "" {
			return "✓ Команда выполнена"
		}
		return "✓ Команда выполнена · " + ruCount(utf8.RuneCountInString(body), "знак", "знака", "знаков")
	default:
		return ""
	}
}

func searchHitHint(body string) (int, string) {
	var hits []struct {
		Title string `json:"title"`
		URL   string `json:"url"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(body)), &hits); err == nil && len(hits) > 0 {
		var titles []string
		for _, h := range hits {
			t := strings.TrimSpace(h.Title)
			if t == "" {
				t = shortURL(h.URL)
			}
			if t == "" {
				continue
			}
			titles = append(titles, clipStatus(t, 40))
			if len(titles) == 3 {
				break
			}
		}
		return len(hits), strings.Join(titles, "; ")
	}
	n := strings.Count(body, `"url"`)
	if n == 0 {
		n = strings.Count(body, "http://") + strings.Count(body, "https://")
	}
	return n, ""
}

func pageTitleHint(body string) string {
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		low := strings.ToLower(t)
		if strings.HasPrefix(low, "source=") || strings.HasPrefix(low, "url=") || strings.HasPrefix(low, "status=") {
			continue
		}
		t = strings.TrimLeft(t, "#>* ")
		if strings.HasPrefix(strings.ToLower(t), "title:") {
			t = strings.TrimSpace(t[6:])
		}
		t = strings.Trim(t, "[]()\"'")
		if t == "" || strings.HasPrefix(t, "http") {
			continue
		}
		return clipStatus(t, 70)
	}
	return ""
}

func firstArg(args map[string]any, keys ...string) string {
	if args == nil {
		return ""
	}
	for _, k := range keys {
		if s := argString(args, k); s != "" {
			return s
		}
	}
	return ""
}

func shortURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return clipStatus(raw, 70)
	}
	host := strings.TrimPrefix(u.Host, "www.")
	p := strings.TrimSuffix(u.Path, "/")
	if p == "" {
		return host
	}
	if utf8.RuneCountInString(p) > 36 {
		return host + "/…/" + path.Base(p)
	}
	return host + p
}

func shortPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	base := filepath.Base(p)
	if base == "." || base == string(filepath.Separator) {
		return clipStatus(p, 50)
	}
	return base
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func clipStatus(s string, n int) string {
	s = oneLine(s)
	if n <= 0 || utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	if n < 2 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

func ruCount(n int, one, few, many string) string {
	if n < 0 {
		n = 0
	}
	return formatInt(n) + " " + ruNoun(n, one, few, many)
}

func ruNoun(n int, one, few, many string) string {
	n = absInt(n) % 100
	if n >= 11 && n <= 14 {
		return many
	}
	switch n % 10 {
	case 1:
		return one
	case 2, 3, 4:
		return few
	default:
		return many
	}
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func formatInt(n int) string {
	if n < 1000 {
		return itoa(n)
	}
	s := itoa(n)
	var b strings.Builder
	lead := len(s) % 3
	if lead == 0 {
		lead = 3
	}
	b.WriteString(s[:lead])
	for i := lead; i < len(s); i += 3 {
		b.WriteByte(' ')
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
