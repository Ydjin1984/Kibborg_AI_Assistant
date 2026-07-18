package main

// Live activity tracker feeding the web UI "state window": what the engine is doing right
// now (idle / prompt / generating), a live tokens-per-second estimate while a reply streams,
// and the exact llama.cpp timings of the last completed generation.

import (
	"sync"
	"time"
)

// GenStats holds llama.cpp's own timing numbers for one generation (source of truth for
// the displayed speeds — computed by the server, not guessed).
type GenStats struct {
	PromptTokens int     `json:"prompt_tokens"`
	PromptPerSec float64 `json:"prompt_per_sec"`
	GenTokens    int     `json:"gen_tokens"`
	GenPerSec    float64 `json:"gen_per_sec"`
	TotalMs      float64 `json:"total_ms"`
}

// activity is the single process-wide state the status window reads.
type activity struct {
	mu        sync.Mutex
	phase     string    // idle | generating
	label     string    // human tag: "чат", "анализ BTC", "браузер"…
	startedAt time.Time // start of the current generation
	tokens    int       // tokens streamed so far this generation (live estimate)
	last      GenStats  // exact stats of the last finished generation
	lastLabel string
	lastAt    time.Time
}

var live = &activity{phase: "idle"}

// begin marks the start of a generation for a given label.
func (a *activity) begin(label string) {
	a.mu.Lock()
	a.phase = "generating"
	a.label = label
	a.startedAt = time.Now()
	a.tokens = 0
	a.mu.Unlock()
}

// tick bumps the live token counter (called per streamed chunk ≈ per token).
func (a *activity) tick() {
	a.mu.Lock()
	a.tokens++
	a.mu.Unlock()
}

// finish records the final stats and returns to idle.
func (a *activity) finish(s GenStats) {
	a.mu.Lock()
	a.phase = "idle"
	a.last = s
	a.lastLabel = a.label
	a.lastAt = time.Now()
	a.label = ""
	a.mu.Unlock()
}

// snapshot returns a JSON-friendly view of the current + last-known activity.
func (a *activity) snapshot() map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	m := map[string]any{
		"phase": a.phase,
		"label": a.label,
	}
	if a.phase == "generating" {
		elapsed := time.Since(a.startedAt).Seconds()
		m["elapsed_sec"] = round1(elapsed)
		m["live_tokens"] = a.tokens
		if elapsed > 0.2 {
			m["live_tok_per_sec"] = round1(float64(a.tokens) / elapsed)
		}
	}
	m["last"] = map[string]any{
		"label":         a.lastLabel,
		"prompt_tokens": a.last.PromptTokens,
		"prompt_tok_s":  round1(a.last.PromptPerSec),
		"gen_tokens":    a.last.GenTokens,
		"gen_tok_s":     round1(a.last.GenPerSec),
		"total_ms":      round1(a.last.TotalMs),
		"has":           a.last.GenTokens > 0,
	}
	return m
}

func round1(f float64) float64 {
	return float64(int(f*10+0.5)) / 10
}
