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
	ls.rememberAct("поиск «btc news»")
	ls.rememberAct("cointelegraph.com")
	got := ls.thinkLine()
	if !strings.Contains(got, "btc news") || !strings.Contains(got, "cointelegraph.com") {
		t.Fatalf("обдум без собранного: %q", got)
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
}
