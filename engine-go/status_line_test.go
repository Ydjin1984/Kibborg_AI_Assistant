package main

import (
	"strings"
	"testing"
)

func TestToolStatusLineShowsQueryAndURL(t *testing.T) {
	got := toolStatusLine("web_search", map[string]any{"query": "новости биткоина сегодня"})
	if !strings.Contains(got, "Ищу") || !strings.Contains(got, "новости биткоина") {
		t.Fatalf("поиск без запроса: %q", got)
	}
	got = toolStatusLine("read_url", map[string]any{"url": "https://www.cointelegraph.com/news/btc-etf"})
	if !strings.Contains(got, "Читаю") || !strings.Contains(got, "cointelegraph.com") {
		t.Fatalf("чтение без URL: %q", got)
	}
	if strings.Contains(got, "www.") {
		t.Fatalf("www не срезан: %q", got)
	}
	got = toolStatusLine("read_file", map[string]any{"path": `D:\work\report.pdf`})
	if !strings.Contains(got, "report.pdf") {
		t.Fatalf("файл без имени: %q", got)
	}
	got = toolStatusLine("analyze_ticker", map[string]any{"symbol": "btcusdt"})
	if !strings.Contains(got, "BTCUSDT") {
		t.Fatalf("тикер: %q", got)
	}
}

func TestToolStatusWordFallbackStillHuman(t *testing.T) {
	if got := toolStatusWord("web_search"); !strings.Contains(got, "Ищу") {
		t.Fatalf("фолбэк поиска: %q", got)
	}
	if got := toolStatusWord("read_url"); !strings.Contains(got, "Читаю") {
		t.Fatalf("фолбэк чтения: %q", got)
	}
}

func TestProbeURLStatusShowsHostAndFindings(t *testing.T) {
	got := toolStatusLine("probe_url", map[string]any{"url": "https://profi.sysx.uz/api/v1/staff"})
	if !strings.Contains(got, "probe_url") || !strings.Contains(got, "profi.sysx.uz") {
		t.Fatalf("статус пробы без URL: %q", got)
	}
	body := "🛡 **HTTP-проба** `https://profi.sysx.uz/api/v1/staff`\n" +
		"- статус: 200 · HTTPS: true · TLS: TLS1.3\n\n**Находки:**\n" +
		"1. [HIGH] **Missing auth**\n   где: /api/v1/staff\n" +
		"2. [INFO] **SPA soft-404**\n   где: /\n"
	got = toolResultLine("probe_url", map[string]any{"url": "https://profi.sysx.uz/api/v1/staff"},
		ToolResult{Status: StatusOK, Text: body})
	if !strings.Contains(got, "200") || !strings.Contains(got, "наход") {
		t.Fatalf("итог пробы: %q", got)
	}
	got = toolStatusLine("write_security_report", map[string]any{"target": "https://profi.sysx.uz"})
	if !strings.Contains(got, "отчёт") || !strings.Contains(got, "profi.sysx.uz") {
		t.Fatalf("статус отчёта: %q", got)
	}
}

func TestSearchHitHintFromJSON(t *testing.T) {
	body := `[{"title":"Bitcoin ETF inflows","url":"https://a.example/1"},{"title":"ETH update","url":"https://b.example/2"}]`
	n, titles := searchHitHint(body)
	if n != 2 {
		t.Fatalf("n=%d", n)
	}
	if !strings.Contains(titles, "Bitcoin ETF") || !strings.Contains(titles, "ETH") {
		t.Fatalf("заголовки: %q", titles)
	}
}

func TestPageTitleHintSkipsMeta(t *testing.T) {
	body := "source=jina\nurl=https://example.com/a\n\n# Bitcoin hits new high\n\ntext"
	if got := pageTitleHint(body); got != "Bitcoin hits new high" {
		t.Fatalf("title=%q", got)
	}
}

func TestToolResultLineSearchAndRead(t *testing.T) {
	hits := `[{"title":"One","url":"https://a.test/x"},{"title":"Two","url":"https://b.test/y"}]`
	got := toolResultLine("web_search", map[string]any{"query": "btc"}, ToolResult{Status: StatusOK, Text: hits})
	if !strings.Contains(got, "Нашёл") || !strings.Contains(got, "One") {
		t.Fatalf("итог поиска: %q", got)
	}
	got = toolResultLine("read_url", map[string]any{"url": "https://a.test/story"}, ToolResult{
		Status: StatusOK,
		Text:   "source=jina\nurl=https://a.test/story\n\n# The story\n\n" + strings.Repeat("слово ", 200),
	})
	if !strings.Contains(got, "a.test") || !strings.Contains(got, "The story") {
		t.Fatalf("итог чтения: %q", got)
	}
	got = toolResultLine("read_url", nil, ToolResult{Status: StatusFailed, Text: "HTTP 403"})
	if !strings.Contains(got, "Не вышло") || !strings.Contains(got, "403") {
		t.Fatalf("провал: %q", got)
	}
}

func TestThinkLineNamesWhatWasRead(t *testing.T) {
	ls := &loopState{task: &Task{Input: "новости крипты"}}
	if got := ls.thinkLine(); !strings.Contains(got, "новости крипты") {
		t.Fatalf("пустой обдум: %q", got)
	}
	if strings.Contains(ls.thinkLine(), "мозг") {
		t.Fatalf("слово «мозг» только в badge UI, не в тексте: %q", ls.thinkLine())
	}
	ls.rememberAct("поиск «btc news»")
	ls.rememberAct("cointelegraph.com")
	got := ls.thinkLine()
	if !strings.Contains(got, "btc news") || !strings.Contains(got, "cointelegraph.com") {
		t.Fatalf("обдум без собранного: %q", got)
	}
}

func TestToolStatusLineIncludesToolName(t *testing.T) {
	got := toolStatusLine("download_url", map[string]any{"url": "https://profi.sysx.uz/api/v1/staff"})
	if !strings.Contains(got, "`download_url`") || !strings.Contains(got, "profi.sysx.uz") {
		t.Fatalf("имя tool должно быть видно: %q", got)
	}
}

func TestToolResultLineShowsContentNotOnlyChars(t *testing.T) {
	links := "[\n  {\"text\":\"Staff\",\"href\":\"https://profi.sysx.uz/api/v1/staff\"},\n  {\"text\":\"Home\",\"href\":\"https://profi.sysx.uz/\"}\n]"
	got := toolResultLine("extract_links", nil, ToolResult{Status: StatusOK, Text: links})
	if strings.Contains(got, "знак") {
		t.Fatalf("не должны показывать только счётчик знаков: %q", got)
	}
	if !strings.Contains(got, "ссыл") || !strings.Contains(got, "profi.sysx.uz") {
		t.Fatalf("ожидали превью ссылок: %q", got)
	}
	u := statusResultBody("extract_links", "", got, links)
	if !strings.Contains(u.Preview, "Staff") && !strings.Contains(u.Preview, "href") {
		t.Fatalf("preview должен содержать начало ответа: %q", u.Preview)
	}
	if u.Body == "" {
		t.Fatal("body для разворота пустой")
	}
	lines := strings.Split(u.Preview, "\n")
	if len(lines) > toolResultPreviewLines {
		t.Fatalf("preview > %d строк: %d", toolResultPreviewLines, len(lines))
	}
}

func TestFirstNLines(t *testing.T) {
	got := firstNLines("a\n\nb\nc\nd", 3)
	if got != "a\n\nb" && got != "a\nb\nc" {
		// empty line after start is kept once we started
		if strings.Count(got, "\n")+1 > 3 {
			t.Fatalf("too many lines: %q", got)
		}
	}
	if firstNLines("", 3) != "" {
		t.Fatal("empty")
	}
}

func TestToolParallelOKList(t *testing.T) {
	if !toolParallelOK("probe_url") || !toolParallelOK("download_url") || !toolParallelOK("http_get") {
		t.Fatal("сетевые evidence-тулы должны параллелиться")
	}
	if toolParallelOK("run_command") || toolParallelOK("write_file") || toolParallelOK("click_element") || toolParallelOK("web_search") {
		t.Fatal("мутаторы и Chrome-search не должны идти в parallel batch")
	}
}

func TestWebTraceKeepsFullStatus(t *testing.T) {
	html := string(webIndexHTML)
	if strings.Contains(html, "ev.status.slice(0, 90)") {
		t.Fatal("лента шагов снова режет статус до 90 символов")
	}
	if !strings.Contains(html, "overflow-wrap:anywhere") {
		t.Fatal("длинный URL в ленте должен переноситься")
	}
	if !strings.Contains(html, "trace.shut") || !strings.Contains(html, "Формулирую ответ") {
		t.Fatal("после ответа лента шагов должна сворачиваться")
	}
	if !strings.Contains(html, "pushTraceStatus") || !strings.Contains(html, "tooltag") {
		t.Fatal("лента должна рисовать badge имени tool из ev.tool")
	}
	if !strings.Contains(html, "workLabelFromEv") {
		t.Fatal("живой статус сверху должен показывать tool → args")
	}
	if !strings.Contains(html, "развернуть ответ") || !strings.Contains(html, "meta.preview") {
		t.Fatal("лента должна показывать превью ответа и разворот body")
	}
}
