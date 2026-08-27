package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// parseToolArgs turns the OpenAI-style Arguments JSON string into a map.
// Local models often truncate mid-string (long curl + Bearer JWT) or leave the
// object unclosed; we repair that before giving up so the tool can still run
// and — more importantly — so the transcript stays legal for the next llama-server turn.
func parseToolArgs(raw string) (map[string]any, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return map[string]any{}, nil
	}
	args := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &args); err == nil {
		return args, nil
	}
	repaired := repairToolArgsJSON(raw)
	if repaired != raw {
		if err := json.Unmarshal([]byte(repaired), &args); err == nil {
			return args, nil
		}
	}
	return nil, fmt.Errorf("не разобрал JSON аргументов: unexpected end of JSON input. " +
		"Для HTTP API с токеном используй http_get(url, authorization=\"Bearer …\"), " +
		"а не curl внутри run_command — длинный JWT ломает кавычки tool-call")
}

// ensureValidToolArgsJSON returns Arguments that json.Valid accepts.
// Unrepairable junk becomes {} so assistant tool_calls never poison the next LLM request
// (llama-server answers HTTP 500: Failed to parse tool call arguments as JSON).
func ensureValidToolArgsJSON(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return "{}"
	}
	if json.Valid([]byte(raw)) {
		return raw
	}
	repaired := repairToolArgsJSON(raw)
	if json.Valid([]byte(repaired)) {
		return repaired
	}
	return "{}"
}

// repairToolArgsJSON closes a truncated JSON object/array: open strings, braces, brackets.
// It does not invent missing keys — only finishes the structural wrapper so Unmarshal can run.
func repairToolArgsJSON(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "{}"
	}
	inString := false
	escape := false
	stack := make([]byte, 0, 4)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inString {
			if escape {
				escape = false
				continue
			}
			if c == '\\' {
				escape = true
				continue
			}
			if c == '"' {
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{', '[':
			stack = append(stack, c)
		case '}':
			if n := len(stack); n > 0 && stack[n-1] == '{' {
				stack = stack[:n-1]
			}
		case ']':
			if n := len(stack); n > 0 && stack[n-1] == '[' {
				stack = stack[:n-1]
			}
		}
	}
	out := s
	if escape && strings.HasSuffix(out, `\`) {
		out = out[:len(out)-1]
	}
	if inString {
		out += `"`
	}
	out = strings.TrimRight(out, " \t\r\n")
	for strings.HasSuffix(out, ",") {
		out = strings.TrimSuffix(out, ",")
		out = strings.TrimRight(out, " \t\r\n")
	}
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i] == '{' {
			out += "}"
		} else {
			out += "]"
		}
	}
	return out
}

// sanitizeToolCalls repairs Arguments in place so the wire transcript stays JSON-legal.
func sanitizeToolCalls(calls []toolCall) []toolCall {
	for i := range calls {
		before := calls[i].Function.Arguments
		after := ensureValidToolArgsJSON(before)
		if after != before {
			calls[i].Function.Arguments = after
		}
	}
	return calls
}

// sanitizeMsgsToolArgs walks assistant tool_calls already stored as map[string]any and
// repairs broken Arguments strings left by an earlier turn (or by a resume).
func sanitizeMsgsToolArgs(msgs []map[string]any) {
	for _, m := range msgs {
		if role, _ := m["role"].(string); role != "assistant" {
			continue
		}
		rawCalls, ok := m["tool_calls"]
		if !ok || rawCalls == nil {
			continue
		}
		switch calls := rawCalls.(type) {
		case []map[string]any:
			for _, c := range calls {
				fixToolCallMapArgs(c)
			}
		case []any:
			for _, item := range calls {
				if c, ok := item.(map[string]any); ok {
					fixToolCallMapArgs(c)
				}
			}
		}
	}
}

func fixToolCallMapArgs(c map[string]any) {
	fn, _ := c["function"].(map[string]any)
	if fn == nil {
		return
	}
	args, _ := fn["arguments"].(string)
	fixed := ensureValidToolArgsJSON(args)
	if fixed != args {
		fn["arguments"] = fixed
	}
}

// isBadToolCallArgs matches llama-server failing to parse the model's tool-call JSON
// (truncated string / missing quote) — recoverable with a nudge, not a hard task fail.
func isBadToolCallArgs(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "parse tool call arguments") ||
		strings.Contains(s, "tool call arguments as json") ||
		(strings.Contains(s, "json.exception.parse_error") && strings.Contains(s, "tool")) ||
		(strings.Contains(s, "missing closing quote") && strings.Contains(s, "tool"))
}

const badToolArgsNudge = "Предыдущий tool_call сломал JSON аргументов (строка обрезана или без закрывающей кавычки). " +
	"Повтори КОРОЧЕ. Для HTTP API с Bearer-токеном вызывай http_get(url=\"…\", authorization=\"Bearer …\"), " +
	"а НЕ curl/Invoke-WebRequest внутри run_command — длинный JWT внутри shell-команды ломает JSON tool-call."

// jwtLike matches compact JWTs (header.payload.signature) so they never leak into chat errors.
var jwtLike = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`)

// bearerTokenLike catches "Bearer <long-token>" even when it is not a JWT.
var bearerTokenLike = regexp.MustCompile(`(?i)Bearer\s+[A-Za-z0-9._\-+=/]{16,}`)

// scrubSecrets strips JWTs / Bearer tokens from user-facing and log strings.
// Used on top of redact() (bot token) when LLM error bodies echo the model's broken args.
func scrubSecrets(s string) string {
	s = redact(s)
	s = jwtLike.ReplaceAllString(s, "eyJ***")
	s = bearerTokenLike.ReplaceAllString(s, "Bearer ***")
	return s
}
