package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestParseChartCommand(t *testing.T) {
	cases := []struct {
		in    string
		want  bool
		extra string
	}{
		{"/chart", true, ""},
		{"/chart btc 15m", true, "btc 15m"},
		// Regression: leading whitespace used to shift the slice and mangle the extra
		// text ("  /chart btc" returned "rt btc").
		{"  /chart btc", true, "btc"},
		{"/Анализ eth", true, "eth"},
		{"просто текст", false, ""},
		{"/charting", false, ""},
	}
	for _, c := range cases {
		ok, extra := parseChartCommand(c.in)
		if ok != c.want || extra != c.extra {
			t.Errorf("parseChartCommand(%q) = (%v, %q), want (%v, %q)", c.in, ok, extra, c.want, c.extra)
		}
	}
}

func TestParseBrowserCommand(t *testing.T) {
	ok, task := parseBrowserCommand("  /browser собери таблицу")
	if !ok || task != "собери таблицу" {
		t.Errorf("parseBrowserCommand = (%v, %q)", ok, task)
	}
}

// Telegram groups rewrite commands as /cmd@BotUsername — must still match.
func TestParseCommandBotSuffix(t *testing.T) {
	ok, rest := parseCommand("/download@KibborgBot https://youtu.be/x", downloadCommands)
	if !ok || rest != "https://youtu.be/x" {
		t.Errorf("parseCommand bot suffix = (%v, %q)", ok, rest)
	}
	ok, rest = parseCommand("/browser@MyBot открой вкладку", browserCommands)
	if !ok || rest != "открой вкладку" {
		t.Errorf("parseCommand browser bot suffix = (%v, %q)", ok, rest)
	}
	ok, _ = parseCommand("/downloading https://x", downloadCommands)
	if ok {
		t.Error("prefix of another word must not match")
	}
}

func TestPathUnderRoot(t *testing.T) {
	root := `D:\proj\runtime\browser`
	if !pathUnderRoot(`D:\proj\runtime\browser\videos\a.mp4`, root) {
		t.Error("file under root should be allowed")
	}
	if pathUnderRoot(`D:\proj\runtime\browser_evil\secret`, root) {
		t.Error("browser_evil must not match as under browser")
	}
	if !pathUnderRoot(root, root) {
		t.Error("root itself should be allowed")
	}
}

// Regression: splitMessage used to cut byte-wise and could split a multi-byte
// UTF-8 rune in half — Telegram rejects messages with invalid UTF-8.
func TestSplitMessageKeepsValidUTF8(t *testing.T) {
	text := strings.Repeat("ыэюя", 2000) // 16000 bytes, no newlines
	for _, chunk := range splitMessage(text, 3001) {
		if !utf8.ValidString(chunk) {
			t.Fatalf("chunk is not valid UTF-8: %q…", chunk[:12])
		}
	}
	// splitting must not lose content
	if got := strings.Join(splitMessage(text, 3001), ""); got != text {
		t.Fatal("splitMessage lost or reordered content")
	}
}

// A long fenced code block must not be torn across chunks with a dangling ``` — every chunk
// must carry a balanced (even) number of fences so each renders as valid Markdown alone.
func TestSplitMessageKeepsCodeFences(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("Вот код:\n```go\n")
	for i := 0; i < 400; i++ {
		sb.WriteString("func f() { doSomethingRelativelyLongHere() } // строка\n")
	}
	sb.WriteString("```\nГотово.")
	chunks := splitMessage(sb.String(), 3000)
	if len(chunks) < 2 {
		t.Fatalf("expected the long block to span multiple chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		if strings.Count(c, "```")%2 != 0 {
			t.Errorf("chunk %d has unbalanced code fences:\n%s", i, c)
		}
	}
}
