package main

// Chat history persistence: the in-memory sliding windows survive bot restarts. A background
// saver flushes dirty state every few seconds; shutdown (see shutdown.go) flushes once more.
// runtime/ is gitignored, so the dialog log never lands in the repo.

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"time"
)

const historyFile = "runtime/history.json"

// histDirty is guarded by histMu (shared with history itself).
var histDirty bool

// markHistoryDirty must be called with histMu held.
func markHistoryDirty() {
	histDirty = true
}

// loadHistory restores the per-chat windows from disk at startup (missing file = fresh start).
func loadHistory() {
	data, err := os.ReadFile(historyFile)
	if err != nil {
		return
	}
	var stored map[int64][]chatMsg
	if err := json.Unmarshal(data, &stored); err != nil {
		log.Printf("[HISTORY] %s повреждён (%v) — начинаю с чистой истории", historyFile, err)
		return
	}
	// A file containing literal `null` unmarshals into a nil map WITHOUT error. Keeping it
	// would make the first recordHistory panic on "assignment to entry in nil map".
	if stored == nil {
		log.Printf("[HISTORY] %s пуст (null) — начинаю с чистой истории", historyFile)
		return
	}
	histMu.Lock()
	history = stored
	histMu.Unlock()
	log.Printf("[HISTORY] восстановлена история %d чатов", len(stored))
}

// saveHistoryNow writes the current history to disk if it changed since the last save.
// Write-to-temp + rename keeps the file intact if the process dies mid-write.
//
// Dirty is cleared under the lock right after we take a marshal snapshot. If the disk
// write fails we re-dirty so the next tick retries (avoids silent permanent loss).
// Concurrent recordHistory after the clear re-sets dirty for any post-snapshot mutation.
func saveHistoryNow() {
	histMu.Lock()
	if !histDirty {
		histMu.Unlock()
		return
	}
	data, err := json.Marshal(history)
	if err != nil {
		histMu.Unlock()
		log.Printf("[HISTORY] marshal error: %v", err)
		return
	}
	histDirty = false
	histMu.Unlock()

	if err := os.MkdirAll(filepath.Dir(historyFile), 0o755); err != nil {
		log.Printf("[HISTORY] mkdir error: %v", err)
		histMu.Lock()
		histDirty = true
		histMu.Unlock()
		return
	}
	tmp := historyFile + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		log.Printf("[HISTORY] write error: %v", err)
		histMu.Lock()
		histDirty = true
		histMu.Unlock()
		return
	}
	if err := os.Rename(tmp, historyFile); err != nil {
		log.Printf("[HISTORY] rename error: %v", err)
		histMu.Lock()
		histDirty = true
		histMu.Unlock()
	}
}

// startHistorySaver flushes dirty history to disk every 10 seconds.
func startHistorySaver() {
	go func() {
		for range time.Tick(10 * time.Second) {
			saveHistoryNow()
		}
	}()
}
