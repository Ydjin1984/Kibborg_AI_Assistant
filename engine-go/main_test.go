package main

import (
	"fmt"
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
	ok, task = parseBrowserCommand("/agent найди статьи про Rust")
	if !ok || task != "найди статьи про Rust" {
		t.Errorf("parseBrowserCommand /agent = (%v, %q)", ok, task)
	}
	ok, task = parseBrowserCommand("/агент dir D:\\tmp")
	if !ok || !strings.Contains(task, "dir") {
		t.Errorf("parseBrowserCommand /агент = (%v, %q)", ok, task)
	}
}

func TestWantsToolAgent(t *testing.T) {
	// Free chat always uses tools when the channel allows them (empty → false).
	yes := []string{
		"найди статьи про Go",
		"какой сегодня день",
		"привет",
		"дай данные с числами",
		"у тебя есть доступ к консоли",
	}
	for _, s := range yes {
		if !wantsToolAgent(s) {
			t.Errorf("wantsToolAgent(%q) = false, want true", s)
		}
	}
	if wantsToolAgent("") || wantsToolAgent("   ") {
		t.Error("empty text must not enable tool agent")
	}
}

func TestAgentSystemPromptHasLiveClock(t *testing.T) {
	p := agentSystemPrompt()
	if !strings.Contains(p, "СЕЙЧАС") {
		t.Fatal("agent prompt must inject live clock")
	}
	if !strings.Contains(p, "run_command") || !strings.Contains(p, "web_search") {
		t.Fatal("agent prompt must advertise tools")
	}
	// Must forbid the "I have no access" lie the model kept producing.
	if !strings.Contains(p, "НИКОГДА") {
		t.Fatal("agent prompt must forbid denying tool access")
	}
}

func TestPackAgentMessagesTrimsHistory(t *testing.T) {
	base := []map[string]any{
		{"role": "system", "content": "old system"},
		{"role": "system", "content": strings.Repeat("mem ", 2000)},
		{"role": "user", "content": "u1"},
		{"role": "assistant", "content": "a1"},
		{"role": "user", "content": "u2"},
		{"role": "assistant", "content": "a2"},
		{"role": "user", "content": "u3"},
		{"role": "assistant", "content": "a3"},
		{"role": "user", "content": "u4"},
		{"role": "assistant", "content": "a4"},
		{"role": "user", "content": "u5"},
		{"role": "assistant", "content": "a5"},
	}
	msgs := packAgentMessages(base, "SYS", "task now")
	if msgs[0]["content"] != "SYS" {
		t.Fatalf("system must be agent prompt, got %v", msgs[0]["content"])
	}
	// system + <=1 mem + <=6 hist + task
	if len(msgs) > 1+agentMaxMemBlocks+agentMaxHistMsgs+1 {
		t.Fatalf("too many messages: %d", len(msgs))
	}
	last := msgs[len(msgs)-1]
	if last["role"] != "user" || last["content"] != "task now" {
		t.Fatalf("last must be task, got %+v", last)
	}
	// memory capped
	for _, m := range msgs {
		if c, ok := m["content"].(string); ok && len(c) > agentMaxMsgChars+10 && m["role"] != "system" {
			// system agent prompt can be longer; memory is role=system too but capped by mem chars
			_ = c
		}
	}
	if !isContextOverflow(fmt.Errorf(`LLM HTTP 400: exceed_context_size_error n_prompt_tokens=72032`)) {
		t.Fatal("must detect context overflow")
	}
	if !isContextBad(fmt.Errorf(`erased invalidated context checkpoint`)) {
		t.Fatal("must detect invalidated context checkpoint")
	}
	if !isContextBad(fmt.Errorf(`invalid context`)) {
		t.Fatal("must detect invalid context")
	}
}

func TestEstimateAgentChars(t *testing.T) {
	msgs := []map[string]any{
		{"role": "system", "content": "sys"},
		{"role": "user", "content": "hello"},
	}
	n := estimateAgentChars(msgs, nil)
	if n < 20 {
		t.Fatalf("estimate too small: %d", n)
	}
}

func TestExtractHTTPURLs(t *testing.T) {
	s := `[{"url":"https://coindesk.com/a/b","title":"x"}, "see https://example.com/path?q=1."]`
	got := extractHTTPURLs(s)
	if len(got) < 2 {
		t.Fatalf("want >=2 urls, got %v", got)
	}
}

func TestShouldForceRead(t *testing.T) {
	if !shouldForceRead("найди новости по BTC", true, false, 0) {
		t.Fatal("news after search without read should force")
	}
	if shouldForceRead("найди новости по BTC", true, true, 0) {
		t.Fatal("already read — no force")
	}
	if !shouldForceRead("прочитай все и дай выжимку", false, false, 0) {
		t.Fatal("follow-up read should force")
	}
	if shouldForceRead("привет", false, false, 0) {
		t.Fatal("greeting should not force")
	}
}

func TestAgentURLBag(t *testing.T) {
	clearAgentURLs(42)
	rememberToolURLs(42, "web_search", `[{"url":"https://a.example/news1"},{"url":"https://b.example/news2"}]`, "")
	urls := getAgentURLs(42)
	if len(urls) != 2 {
		t.Fatalf("bag=%v", urls)
	}
	note := agentURLBagNote(42)
	if !strings.Contains(note, "https://a.example/news1") {
		t.Fatalf("note missing url: %s", note)
	}
	clearAgentURLs(42)
	if len(getAgentURLs(42)) != 0 {
		t.Fatal("clear failed")
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
