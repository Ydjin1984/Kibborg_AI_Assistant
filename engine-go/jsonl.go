package main

// Append-only JSONL journals with size rotation (ТЗ §6.5). Two files live here:
//
//	runtime/hands.jsonl — one line per tool-call decision (why the guard said yes/no)
//	runtime/tasks.jsonl — one line per finished task (input → packs → tools → outcome)
//
// They answer different questions: hands.jsonl says "почему он спросил?", tasks.jsonl says
// "почему он вчера сделал X". taskID joins them.

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
)

const (
	handsLogPath = "runtime/hands.jsonl"
	tasksLogPath = "runtime/tasks.jsonl"
	// jsonlRotateBytes: at 5 MB the current file becomes *.jsonl.1 (one generation kept)
	// and a fresh one starts. /logs without an argument reads the current file.
	jsonlRotateBytes = 5 << 20
)

var jsonlMu sync.Mutex

// appendJSONL writes one compact JSON line, rotating the file first if it grew past the cap.
// Journal failures never break a task: they are logged and swallowed.
func appendJSONL(path string, rec any) {
	line, err := json.Marshal(rec)
	if err != nil {
		log.Printf("[JSONL] marshal %s: %v", path, err)
		return
	}
	jsonlMu.Lock()
	defer jsonlMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.Printf("[JSONL] mkdir %s: %v", filepath.Dir(path), err)
		return
	}
	rotateJSONL(path)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		log.Printf("[JSONL] open %s: %v", path, err)
		return
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		log.Printf("[JSONL] write %s: %v", path, err)
	}
}

// rotateJSONL moves an oversized journal to <path>.1 (replacing the previous generation).
// Caller must hold jsonlMu.
func rotateJSONL(path string) {
	st, err := os.Stat(path)
	if err != nil || st.Size() < jsonlRotateBytes {
		return
	}
	prev := path + ".1"
	_ = os.Remove(prev)
	if err := os.Rename(path, prev); err != nil {
		log.Printf("[JSONL] rotate %s: %v", path, err)
	}
}

// handsRecord is one guard decision (§6.5). risk/rule/reason come straight from Decision —
// without `rule` the journal answers "спросил", but not "почему спросил".
type handsRecord struct {
	TS       string `json:"ts"`
	TaskID   string `json:"taskID"`
	Channel  string `json:"channel"`
	ChatID   int64  `json:"chatID"`
	Mode     string `json:"mode"`
	Tool     string `json:"tool"`
	ArgsCap  string `json:"args_cap"`
	Decision string `json:"decision"` // auto | asked | approved | denied | blocked
	Risk     string `json:"risk,omitempty"`
	Rule     string `json:"rule,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Status   string `json:"status,omitempty"` // ResultStatus once executed
	Exit     string `json:"exit,omitempty"`   // short error text, if any
}

// taskRecord is one finished task (§6.5).
type taskRecord struct {
	TS           string   `json:"ts"`
	TaskID       string   `json:"taskID"`
	Channel      string   `json:"channel"`
	ChatID       int64    `json:"chatID"`
	InputCap     string   `json:"input_cap"`
	Packs        []string `json:"packs"`
	Escalations  int      `json:"escalations"`
	Steps        int      `json:"steps"`
	Tools        []string `json:"tools"`
	Status       string   `json:"status"`
	DispatcherMs int64    `json:"dispatcher_ms"`
	TotalMs      int64    `json:"total_ms"`
	// FirstPromptMs/Tokens is §3.2's measurement: how long llama-server spent on the prompt
	// of the FIRST executor turn. Together with dispatcher_ms and total_ms this answers
	// «где тормозит» without a twenty-metric telemetry stand (§14).
	FirstPromptMs     int64 `json:"first_prompt_ms,omitempty"`
	FirstPromptTokens int   `json:"first_prompt_tokens,omitempty"`
	// ProseCalls: how many calls had to be recovered from prose this task (see prose_calls.go).
	ProseCalls int `json:"prose_calls,omitempty"`
	Artifacts  int `json:"artifacts"`
	// DispatcherRaw keeps the dispatcher's own JSON — the honest replacement for a
	// `confidence` field the local model would only ever set to 0.9 (§14).
	DispatcherRaw string `json:"dispatcher_raw,omitempty"`
}
