package main

// Memory wiring: connects the SQLite memory.Store to the chat pipeline. Two hooks matter:
//   1. On the way IN  — withMemory() injects the rolling summary + the most relevant past
//      episodes right after the system prompt, so the model answers with real recalled
//      context instead of guessing (anti-hallucination by grounding).
//   2. On the way OUT — rememberExchange() stores every completed turn (verbatim, with an
//      optional embedding) and periodically rebuilds the rolling summary.
// Everything degrades gracefully: no DB → chat behaves exactly as before; no embed model →
// recall uses keyword matching instead of vectors.

import (
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"kibborg/engine/memory"
)

const memDBPath = "runtime/memory.db"

// memRecallK is how many past episodes we surface into the prompt per turn.
const memRecallK = 4

// summaryEvery rebuilds the rolling summary once this many new exchanges have accumulated.
const summaryEvery = 6

// mem and memConfig are set once by initMemory (before any worker goroutine starts), so plain
// reads from the chat goroutines are safe without a lock.
var (
	mem       *memory.Store
	memConfig Config
)

var (
	summaryMu       sync.Mutex
	summaryCounter  = map[int64]int{}
	summaryBuilding = map[int64]bool{}
)

// initMemory opens the memory database. On any failure it logs and leaves mem nil, so the bot
// runs fine without long-term memory.
func initMemory(cfg Config) {
	if !cfg.MemoryEnabled {
		log.Printf("[MEMORY] выключена (MEMORY_ENABLED=0)")
		return
	}
	if err := os.MkdirAll("runtime", 0o755); err != nil {
		log.Printf("[MEMORY] не создать runtime/: %v — работаю без долговременной памяти", err)
		return
	}
	st, err := memory.Open(memDBPath)
	if err != nil {
		log.Printf("[MEMORY] не открылась БД (%v) — работаю без долговременной памяти", err)
		return
	}
	mem = st
	memConfig = cfg
	log.Printf("[MEMORY] долговременная память включена: %s (векторный recall: %v)", memDBPath, embedEnabled(cfg))
}

// closeMemory flushes/closes the DB at shutdown.
func closeMemory() {
	if mem != nil {
		_ = mem.Close()
	}
}

// withMemory returns base with memory context (summary + recalled episodes) inserted right
// after the system message. base must start with the system message (baseMessages* guarantees
// this). Returns base unchanged when memory is off or nothing relevant is found.
func withMemory(cfg Config, chatID int64, userText string, base []map[string]any) []map[string]any {
	if mem == nil || strings.TrimSpace(userText) == "" || len(base) == 0 {
		return base
	}
	blocks := memoryContext(cfg, chatID, userText)
	if len(blocks) == 0 {
		return base
	}
	out := make([]map[string]any, 0, len(base)+len(blocks))
	out = append(out, base[0]) // system prompt stays first
	out = append(out, blocks...)
	out = append(out, base[1:]...)
	return out
}

// memoryContext builds the summary + recalled-episode system messages for a turn.
func memoryContext(cfg Config, chatID int64, userText string) []map[string]any {
	var blocks []map[string]any
	if sum, err := mem.GetSummary(chatID); err == nil && strings.TrimSpace(sum) != "" {
		blocks = append(blocks, map[string]any{
			"role":    "system",
			"content": "Долговременная память о собеседнике и прошлых диалогах (опирайся на неё, но не выдумывай сверх сказанного):\n" + sum,
		})
	}
	emb, _ := embedText(cfg, userText) // nil when embeddings off → keyword recall
	eps, err := mem.Recall(chatID, emb, userText, memRecallK)
	if err != nil {
		log.Printf("[MEMORY] recall error: %v", err)
	}
	if len(eps) > 0 {
		var b strings.Builder
		b.WriteString("Релевантные фрагменты из прошлых диалогов (используй, если помогают ответить; если не по теме — игнорируй и не ссылайся на них):\n")
		for _, e := range eps {
			b.WriteString(fmt.Sprintf("— [%s] Пользователь: %s\n  Твой ответ: %s\n",
				tsShort(e.TS), capMem(e.User, 300), capMem(e.Assistant, 400)))
		}
		blocks = append(blocks, map[string]any{"role": "system", "content": b.String()})
	}
	return blocks
}

// rememberExchange stores a completed turn and (periodically) refreshes the summary. It runs
// asynchronously so embedding/summarization never delays the user's reply. Called from
// recordHistory, so every channel (Telegram text/image/file, web) is captured uniformly.
func rememberExchange(chatID int64, userText, reply string) {
	if mem == nil {
		return
	}
	cfg := memConfig
	u := strings.TrimSpace(userText)
	r := strings.TrimSpace(stripThink(reply))
	if u == "" || r == "" {
		return
	}
	go func() {
		emb, err := embedText(cfg, u+"\n"+r)
		if err != nil {
			log.Printf("[MEMORY] embed error: %v", err)
		}
		if err := mem.AddEpisode(chatID, u, r, emb); err != nil {
			log.Printf("[MEMORY] add episode error: %v", err)
			return
		}
		maybeUpdateSummary(cfg, chatID)
	}()
}

// maybeUpdateSummary rebuilds the rolling summary every summaryEvery exchanges, one build at a
// time per chat, and only when the brain is available.
func maybeUpdateSummary(cfg Config, chatID int64) {
	summaryMu.Lock()
	summaryCounter[chatID]++
	due := summaryCounter[chatID] >= summaryEvery && !summaryBuilding[chatID]
	if due {
		summaryCounter[chatID] = 0
		summaryBuilding[chatID] = true
	}
	summaryMu.Unlock()
	if !due {
		return
	}
	defer func() {
		summaryMu.Lock()
		summaryBuilding[chatID] = false
		summaryMu.Unlock()
	}()

	if !brainReady(cfg.BrainPort) {
		return
	}
	old, _ := mem.GetSummary(chatID)
	eps, err := mem.RecentEpisodes(chatID, summaryEvery*2)
	if err != nil || len(eps) == 0 {
		return
	}
	newSum, err := summarizeMemory(cfg, old, eps)
	if err != nil {
		log.Printf("[MEMORY] summarize error: %v", err)
		return
	}
	newSum = strings.TrimSpace(stripThink(newSum))
	if newSum == "" {
		return
	}
	if err := mem.SetSummary(chatID, newSum); err != nil {
		log.Printf("[MEMORY] set summary error: %v", err)
	}
}

const summaryPrompt = `Ты ведёшь краткую долговременную память о диалоге. На основе текущей сводки и новых сообщений составь ОБНОВЛЁННУЮ сводку.

Правила:
- Сохраняй устойчивые факты: кто собеседник, его цели, предпочтения, важные решения, повторяющиеся темы, договорённости.
- Отбрасывай мелочи и разовые реплики.
- Ничего не выдумывай — только то, что реально было в диалоге.
- Пиши по-русски, компактно, до 150 слов, простым списком или короткими фразами.
- Верни ТОЛЬКО текст сводки, без вступлений и пояснений.`

// summarizeMemory asks the brain to merge the old summary with recent episodes into a new one.
func summarizeMemory(cfg Config, old string, eps []memory.Episode) (string, error) {
	var b strings.Builder
	if strings.TrimSpace(old) != "" {
		b.WriteString("Текущая сводка:\n")
		b.WriteString(old)
		b.WriteString("\n\n")
	}
	b.WriteString("Новые сообщения диалога:\n")
	for _, e := range eps {
		b.WriteString("Пользователь: ")
		b.WriteString(capMem(e.User, 500))
		b.WriteString("\nАссистент: ")
		b.WriteString(capMem(e.Assistant, 500))
		b.WriteString("\n")
	}
	msgs := []map[string]any{
		{"role": "system", "content": summaryPrompt},
		{"role": "user", "content": b.String()},
	}
	return llmChat(cfg.BrainPort, msgs, 0.2)
}

// forgetMemory wipes a chat's long-term memory (wired to /reset).
func forgetMemory(chatID int64) {
	if mem == nil {
		return
	}
	if err := mem.Forget(chatID); err != nil {
		log.Printf("[MEMORY] forget error: %v", err)
	}
	summaryMu.Lock()
	delete(summaryCounter, chatID)
	summaryMu.Unlock()
}

// ===== small helpers =====

// capMem truncates text on a rune boundary so injected memory stays compact and valid UTF-8.
func capMem(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

func tsShort(unix int64) string {
	if unix <= 0 {
		return "?"
	}
	return time.Unix(unix, 0).Format("2006-01-02")
}
