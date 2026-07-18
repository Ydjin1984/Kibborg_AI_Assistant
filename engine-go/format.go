package main

import (
	"regexp"
	"strings"
)

// Telegram HTML is far more robust than legacy Markdown for LLM output. We convert the
// model's Markdown to a safe HTML subset Telegram understands: <b>, <i>, <code>, <pre>.

var (
	thinkRe   = regexp.MustCompile(`(?s)<think>.*?</think>`)
	boldRe    = regexp.MustCompile(`\*\*(.+?)\*\*`)
	strikeRe  = regexp.MustCompile(`~~(.+?)~~`)
	linkRe    = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)
	italicRe  = regexp.MustCompile(`\*(.+?)\*`)
	headingRe = regexp.MustCompile(`^#{1,6}\s+(.*)$`)
	langRe    = regexp.MustCompile(`^[a-zA-Z0-9+_-]+$`)
)

// stripThink removes hidden reasoning blocks some models emit (Qwen runs with thinking=1).
func stripThink(s string) string {
	return strings.TrimSpace(thinkRe.ReplaceAllString(s, ""))
}

func htmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// toTelegramHTML converts Markdown to the Telegram HTML subset. Fenced ``` blocks become
// <pre><code>, inline `code` becomes <code>, **bold** → <b>, *italic* → <i>, # headings
// become bold. Underscores are left alone so snake_case identifiers are not italicised.
func toTelegramHTML(md string) string {
	var out strings.Builder
	inFence := false
	fenceLang := ""
	var fence strings.Builder

	flushFence := func() {
		if fenceLang != "" && langRe.MatchString(fenceLang) {
			out.WriteString(`<pre><code class="language-` + fenceLang + `">`)
		} else {
			out.WriteString("<pre><code>")
		}
		out.WriteString(htmlEscape(strings.TrimRight(fence.String(), "\n")))
		out.WriteString("</code></pre>\n")
		fence.Reset()
		fenceLang = ""
	}

	for _, line := range strings.Split(md, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			if inFence {
				flushFence()
				inFence = false
			} else {
				inFence = true
				fenceLang = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "```"))
			}
			continue
		}
		if inFence {
			fence.WriteString(line)
			fence.WriteString("\n")
			continue
		}
		if m := headingRe.FindStringSubmatch(line); m != nil {
			out.WriteString("<b>")
			out.WriteString(renderInline(m[1]))
			out.WriteString("</b>\n")
			continue
		}
		out.WriteString(renderInline(line))
		out.WriteString("\n")
	}
	if inFence {
		flushFence()
	}
	return strings.TrimRight(out.String(), "\n")
}

// renderInline escapes a line, preserving inline `code` spans verbatim, then applies
// bold/italic outside code.
func renderInline(s string) string {
	var b strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == '`' {
			if j := strings.IndexByte(s[i+1:], '`'); j >= 0 {
				b.WriteString("<code>")
				b.WriteString(htmlEscape(s[i+1 : i+1+j]))
				b.WriteString("</code>")
				i = i + 1 + j + 1
				continue
			}
			// unmatched backtick: emit it literally, or the fall-through search below
			// finds this same byte at offset 0 and the loop never advances
			b.WriteByte('`')
			i++
			continue
		}
		// normal text until the next backtick
		seg := s[i:]
		if next := strings.IndexByte(s[i:], '`'); next >= 0 {
			seg = s[i : i+next]
			i += next
		} else {
			i = len(s)
		}
		b.WriteString(renderEmphasis(htmlEscape(seg)))
	}
	return b.String()
}

func renderEmphasis(s string) string {
	s = boldRe.ReplaceAllString(s, "<b>$1</b>")
	s = strikeRe.ReplaceAllString(s, "<s>$1</s>")
	s = linkRe.ReplaceAllString(s, `<a href="$2">$1</a>`)
	s = italicRe.ReplaceAllString(s, "<i>$1</i>")
	return s
}

// stripTags removes HTML tags for the plain-text fallback path.
var tagRe = regexp.MustCompile(`<[^>]+>`)

func stripTags(s string) string {
	s = tagRe.ReplaceAllString(s, "")
	s = strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">").Replace(s)
	return s
}
