package main

// Recovering tool calls the model wrote as PROSE.
//
// This model emits roughly one call in three as text instead of a machine `tool_calls` field:
//
//	run_command(command="Remove-Item -Path 'D:\\tmp\\x' -Recurse -Force", timeout_sec=30)
//
// Nothing then runs, and the user gets a function signature as an "answer". So we parse it and
// execute it — through THE SAME GATE as any real call (guardToolCall → allow/ask/deny/
// hard_block). The gate is what makes this safe: recovering the call changes how the intent
// ARRIVED, never what is permitted.
//
// Strictness matters in both directions: a missed call wastes a turn, but a false positive
// executes something the model only mentioned. Hence the rules below — the name must be an
// ACTIVE tool, the parentheses must balance, and the inside must actually parse as arguments.

import (
	"encoding/json"
	"strconv"
	"strings"

	"kibborg/engine/browser"
)

// maxProseCalls bounds one recovery: a wall of text naming many tools is a red flag, not a
// batch job.
const maxProseCalls = 3

// parseProseToolCalls extracts tool calls written as text. Returns nil when there are none.
func parseProseToolCalls(text string, tools []browser.ToolSpec) []toolCall {
	if strings.TrimSpace(text) == "" || len(tools) == 0 {
		return nil
	}
	// needsArgs: tools with required parameters. `run_command()` with empty parentheses is
	// prose ABOUT the tool, not a call of it — the model cannot mean "run nothing".
	needsArgs := make(map[string]bool, len(tools))
	active := make(map[string]bool, len(tools))
	for _, tl := range tools {
		active[tl.Function.Name] = true
		if req, ok := tl.Function.Parameters["required"].([]string); ok && len(req) > 0 {
			needsArgs[tl.Function.Name] = true
		}
	}

	var out []toolCall
	for i := 0; i < len(text) && len(out) < maxProseCalls; {
		rel := strings.IndexByte(text[i:], '(')
		if rel < 0 {
			break
		}
		open := i + rel
		name := identBefore(text, open)
		end := matchParen(text, open)
		if name == "" || !active[name] || end < 0 {
			i = open + 1
			continue
		}
		args, ok := parseCallArgs(text[open+1 : end])
		if !ok || (len(args) == 0 && needsArgs[name]) {
			i = end + 1
			continue
		}
		tc := toolCall{ID: "prose_" + strconv.Itoa(len(out)) + "_" + name, Type: "function"}
		tc.Function.Name = name
		raw, err := json.Marshal(args)
		if err != nil {
			i = end + 1
			continue
		}
		tc.Function.Arguments = string(raw)
		out = append(out, tc)
		i = end + 1
	}
	return out
}

// identBefore returns the identifier immediately preceding pos, or "".
func identBefore(s string, pos int) string {
	j := pos
	for j > 0 && isIdentByte(s[j-1]) {
		j--
	}
	if j == pos {
		return ""
	}
	// A tool name never starts with a digit; this also rejects `f(2)(x` style noise.
	if s[j] >= '0' && s[j] <= '9' {
		return ""
	}
	return s[j:pos]
}

func isIdentByte(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// matchParen returns the index of the ')' closing the '(' at open, ignoring parentheses inside
// quoted strings. Returns -1 when unbalanced (a truncated generation, for instance).
func matchParen(s string, open int) int {
	depth := 0
	var quote byte
	esc := false
	for k := open; k < len(s); k++ {
		c := s[k]
		if quote != 0 {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == quote:
				quote = 0
			}
			continue
		}
		switch c {
		case '"', '\'':
			quote = c
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return k
			}
		case '\n':
			// A call spanning a blank line is prose, not a call.
			if k+1 < len(s) && s[k+1] == '\n' {
				return -1
			}
		}
	}
	return -1
}

// parseCallArgs reads the inside of the parentheses: either a JSON object or `key=value` pairs.
// ok=false means "this does not look like an argument list" — the safer answer.
func parseCallArgs(src string) (map[string]any, bool) {
	src = strings.TrimSpace(src)
	if src == "" {
		return map[string]any{}, true // a no-arg tool such as list_tabs()
	}
	// JSON form: run_command({"command": "..."})
	if strings.HasPrefix(src, "{") && strings.HasSuffix(src, "}") {
		var m map[string]any
		if err := json.Unmarshal([]byte(src), &m); err == nil {
			return m, true
		}
		return nil, false
	}
	args := map[string]any{}
	for _, part := range splitTopLevel(src, ',') {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		eq := indexTopLevel(part, '=')
		if eq <= 0 {
			return nil, false // positional or prose — refuse rather than guess
		}
		key := strings.TrimSpace(part[:eq])
		if key == "" || !isIdentifier(key) {
			return nil, false
		}
		raw := strings.TrimSpace(part[eq+1:])
		if isPlaceholder(raw) {
			return nil, false // `run_command(command=…)` is documentation, not an instruction
		}
		args[key] = parseArgValue(raw)
	}
	if len(args) == 0 {
		return nil, false
	}
	return args, true
}

// isPlaceholder spots the values models use when EXPLAINING a tool instead of calling it.
// Executing «command=…» would be executing a literal ellipsis.
func isPlaceholder(v string) bool {
	t := strings.Trim(strings.TrimSpace(v), `"'`)
	switch t {
	case "", "…", "...", "..", "-", "_", "?":
		return true
	}
	return (strings.HasPrefix(t, "<") && strings.HasSuffix(t, ">")) ||
		(strings.HasPrefix(t, "{") && strings.HasSuffix(t, "}") && !strings.Contains(t, `"`))
}

func isIdentifier(s string) bool {
	for i := 0; i < len(s); i++ {
		if !isIdentByte(s[i]) {
			return false
		}
	}
	return s != "" && !(s[0] >= '0' && s[0] <= '9')
}

// parseArgValue converts one argument literal into a Go value.
func parseArgValue(v string) any {
	switch v {
	case "true":
		return true
	case "false":
		return false
	case "null", "None", "nil":
		return nil
	}
	if n, err := strconv.ParseFloat(v, 64); err == nil {
		return n
	}
	return unquoteArg(v)
}

// unquoteArg strips quotes, undoing JSON escaping when that is what the model produced.
//
// Windows paths make this delicate: `"D:\\tmp\\x"` must become `D:\tmp\x`, while `"D:\tmp\x"`
// (the model forgot to escape) must NOT become a string with a TAB in it. So an unquote that
// produces control characters is rejected in favour of the literal text.
func unquoteArg(s string) string {
	if len(s) < 2 {
		return s
	}
	q := s[0]
	if (q != '"' && q != '\'') || s[len(s)-1] != q {
		return s
	}
	inner := s[1 : len(s)-1]
	if q == '"' {
		if v, err := strconv.Unquote(s); err == nil && !hasControlChars(v) {
			return v
		}
	}
	return inner
}

func hasControlChars(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 && s[i] != '\n' && s[i] != '\r' {
			return true
		}
	}
	return false
}

// splitTopLevel splits on sep, ignoring separators inside quotes, brackets or braces.
func splitTopLevel(s string, sep byte) []string {
	var out []string
	var quote byte
	esc := false
	depth := 0
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == quote:
				quote = 0
			}
			continue
		}
		switch c {
		case '"', '\'':
			quote = c
		case '[', '{', '(':
			depth++
		case ']', '}', ')':
			depth--
		case sep:
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}

// indexTopLevel finds sep outside quotes/brackets, or -1.
func indexTopLevel(s string, sep byte) int {
	var quote byte
	esc := false
	depth := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == quote:
				quote = 0
			}
			continue
		}
		switch c {
		case '"', '\'':
			quote = c
		case '[', '{', '(':
			depth++
		case ']', '}', ')':
			depth--
		case sep:
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}
