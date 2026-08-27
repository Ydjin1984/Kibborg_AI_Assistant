package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRepairToolArgsJSON_TruncatedCommand(t *testing.T) {
	// Live failure shape: curl + Bearer JWT cut mid-string, missing closing quote/brace.
	raw := `{"command":"curl.exe -sS https://profi.sysx.uz/api/v1/students -H \"Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJpZCI6IjZhOGVkY2Zl`
	fixed := repairToolArgsJSON(raw)
	if !json.Valid([]byte(fixed)) {
		t.Fatalf("repair did not yield valid JSON: %q", fixed)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(fixed), &m); err != nil {
		t.Fatal(err)
	}
	cmd, _ := m["command"].(string)
	if !strings.Contains(cmd, "profi.sysx.uz") {
		t.Fatalf("command lost after repair: %q", cmd)
	}
}

func TestParseToolArgs_RepairsThenReads(t *testing.T) {
	raw := `{"command":"Get-Date","timeout_sec":30`
	args, err := parseToolArgs(raw)
	if err != nil {
		t.Fatal(err)
	}
	if args["command"] != "Get-Date" {
		t.Fatalf("command=%v", args["command"])
	}
}

func TestEnsureValidToolArgsJSON_JunkBecomesObject(t *testing.T) {
	got := ensureValidToolArgsJSON("{не json")
	if !json.Valid([]byte(got)) {
		t.Fatalf("got %q", got)
	}
}

func TestSanitizeToolCalls_FixesBrokenArgs(t *testing.T) {
	tc := toolCall{ID: "x"}
	tc.Function.Name = "run_command"
	tc.Function.Arguments = `{"command":"echo hi`
	out := sanitizeToolCalls([]toolCall{tc})
	if !json.Valid([]byte(out[0].Function.Arguments)) {
		t.Fatalf("still invalid: %q", out[0].Function.Arguments)
	}
}

func TestIsBadToolCallArgs(t *testing.T) {
	err := fmtError(`LLM HTTP 500: {"error":{"message":"Failed to parse tool call arguments as JSON: missing closing quote"}}`)
	if !isBadToolCallArgs(err) {
		t.Fatal("should match llama-server tool-arg parse failure")
	}
	if isBadToolCallArgs(fmtError("LLM HTTP 500: unrelated boom")) {
		t.Fatal("should not match unrelated 500")
	}
}

func TestScrubSecrets_JWTAndBearer(t *testing.T) {
	jwt := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJpZCI6IjZhOGVkY2ZlOTg0MDM3Y2QxMWYzYTg0MSJ9.signaturepart123456"
	in := `curl -H "Authorization: Bearer ` + jwt + `"`
	out := scrubSecrets(in)
	if strings.Contains(out, jwt) {
		t.Fatalf("JWT leaked: %q", out)
	}
	if !strings.Contains(out, "Bearer ***") && !strings.Contains(out, "eyJ***") {
		t.Fatalf("expected redaction marks, got %q", out)
	}
}

// fmtError avoids importing fmt just for Error() in tests that need an error value.
type plainErr string

func (e plainErr) Error() string { return string(e) }

func fmtError(s string) error { return plainErr(s) }
