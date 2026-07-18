package main

import "strings"

import "testing"

func TestToTelegramHTML(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string // substrings that must appear
		deny []string // substrings that must NOT appear
	}{
		{"bold", "это **важно** очень", []string{"<b>важно</b>"}, []string{"**"}},
		{"inline code", "запусти `go build` тут", []string{"<code>go build</code>"}, []string{"`"}},
		{"fenced code", "вот код:\n```go\nfmt.Println(\"hi\")\n```", []string{"<pre><code", "fmt.Println", "</code></pre>"}, []string{"```"}},
		{"escape html in text", "если a < b и c > d & e", []string{"&lt;", "&gt;", "&amp;"}, nil},
		{"snake_case not italic", "поле user_name_value тут", []string{"user_name_value"}, []string{"<i>"}},
		{"heading to bold", "# Заголовок", []string{"<b>Заголовок</b>"}, []string{"#"}},
		{"escape inside code", "`a<b>c`", []string{"<code>a&lt;b&gt;c</code>"}, nil},
		{"link", "смотри [сайт](https://x.io) тут", []string{`<a href="https://x.io">сайт</a>`}, nil},
		{"strikethrough", "это ~~старое~~ ново", []string{"<s>старое</s>"}, []string{"~~"}},
		{"fence with language", "```python\nprint(1)\n```", []string{`<pre><code class="language-python">`, "print(1)"}, []string{"```"}},
		{"divider passes through", "A\n━━━━━━━━━━━━━━━\nB", []string{"━━━━━━━━━━━━━━━"}, nil},
	}
	for _, c := range cases {
		got := toTelegramHTML(c.in)
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: want %q in %q", c.name, w, got)
			}
		}
		for _, d := range c.deny {
			if strings.Contains(got, d) {
				t.Errorf("%s: did NOT want %q in %q", c.name, d, got)
			}
		}
	}
}

// Regression: a single unmatched backtick used to hang renderInline forever
// (the fall-through search found the same byte at offset 0 and never advanced),
// freezing the whole bot since the Telegram poll loop is single-threaded.
func TestRenderInlineUnmatchedBacktick(t *testing.T) {
	got := renderInline("это `код без закрытия")
	if !strings.Contains(got, "`код без закрытия") {
		t.Errorf("unmatched backtick should pass through literally, got %q", got)
	}
}

func TestStripThink(t *testing.T) {
	got := stripThink("<think>рассуждаю тут</think>Ответ пользователю")
	if got != "Ответ пользователю" {
		t.Errorf("stripThink = %q", got)
	}
}

func TestStreamPreview(t *testing.T) {
	if got := streamPreview("<think>x</think>Привет"); got != "Привет" {
		t.Errorf("closed think: %q", got)
	}
	// A reasoning block still being generated must be hidden entirely.
	if got := streamPreview("<think>ещё думаю без закрытия"); got != "" {
		t.Errorf("open think should hide everything: %q", got)
	}
	if got := streamPreview("Ответ <think>hidden</think> дальше"); got != "Ответ  дальше" {
		t.Errorf("inline think: %q", got)
	}
}
