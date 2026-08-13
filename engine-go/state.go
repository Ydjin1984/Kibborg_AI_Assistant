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

	// Ходы агента. Обычный чат стримится и считается потокенно (tick), а цикл инструментов
	// ходит НЕ потоком: там единица наблюдения — законченный ход модели, и её честные цифры
	// приходят из timings llama.cpp. Без этих полей панель во время работы агента показывала
	// нули и выглядела «зависшей».
	turns     int      // сколько ходов модели уже сделано в текущей работе
	turnTok   int      // сгенерировано токенов за все ходы
	lastTurn  GenStats // цифры ПОСЛЕДНЕГО завершённого хода
	ctxTokens int      // размер промпта последнего хода = сколько контекста занято сейчас
}

var live = &activity{phase: "idle"}

// begin marks the start of a generation for a given label.
func (a *activity) begin(label string) {
	a.mu.Lock()
	a.phase = "generating"
	a.label = label
	a.startedAt = time.Now()
	a.tokens = 0
	a.turns = 0
	a.turnTok = 0
	a.mu.Unlock()
}

// tick bumps the live token counter (called per streamed chunk ≈ per token).
func (a *activity) tick() {
	a.mu.Lock()
	a.tokens++
	a.mu.Unlock()
}

// turnDone records one finished NON-streaming model turn (dispatcher, tool loop, summary).
// Цифры настоящие — их посчитал llama.cpp, а не мы.
func (a *activity) turnDone(s GenStats) {
	if s.GenTokens == 0 && s.PromptTokens == 0 {
		return
	}
	a.mu.Lock()
	a.turns++
	a.turnTok += s.GenTokens
	a.lastTurn = s
	if s.PromptTokens > 0 {
		a.ctxTokens = s.PromptTokens
	}
	// Пока идёт цикл инструментов, «последняя генерация» — это последний ход. Иначе после
	// агентской задачи в панели навсегда оставались цифры давнего обычного чата.
	if s.GenTokens > 0 {
		a.last = s
		if a.label != "" {
			a.lastLabel = a.label
		}
		a.lastAt = time.Now()
	}
	a.mu.Unlock()
}

// finish records the final stats and returns to idle. Пустые stats (агентские пути их не
// приносят) НЕ затирают то, что уже насчитали ходы.
func (a *activity) finish(s GenStats) {
	a.mu.Lock()
	a.phase = "idle"
	if s.GenTokens > 0 {
		a.last = s
		a.lastLabel = a.label
		a.lastAt = time.Now()
	}
	a.label = ""
	a.mu.Unlock()
}

// forgetContext обнуляет замер занятого контекста. Вызывается на сбросе диалога: число
// ctxTokens — это prompt_n ПОСЛЕДНЕГО запроса, и после очистки истории оно описывает промпт,
// которого больше нет. Пользователь жал «Сброс» и видел прежние 2.2K — счётчик показывал
// покойника. Скорость генерации при этом не трогаем: она осталась настоящим фактом о железе.
func (a *activity) forgetContext() {
	a.mu.Lock()
	a.ctxTokens = 0
	a.mu.Unlock()
}

// contextTokens is how much of the window the last model call occupied.
func (a *activity) contextTokens() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.ctxTokens
}

// snapshot returns a JSON-friendly view of the current + last-known activity.
func (a *activity) snapshot() map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	m := map[string]any{
		"phase": a.phase,
		"label": a.label,
		"turns": a.turns,
	}
	if a.phase == "generating" {
		elapsed := time.Since(a.startedAt).Seconds()
		m["elapsed_sec"] = round1(elapsed)
		if a.tokens > 0 {
			// Потоковый чат: считаем сами, по мере прихода кусков.
			m["live_tokens"] = a.tokens
			if elapsed > 0.2 {
				m["live_tok_per_sec"] = round1(float64(a.tokens) / elapsed)
			}
		} else if a.turnTok > 0 {
			// Цикл инструментов: потока нет, показываем сумму по завершённым ходам и скорость
			// последнего. Это честные числа llama.cpp, просто обновляются раз в ход, а не
			// раз в токен — так и подписано в интерфейсе.
			m["live_tokens"] = a.turnTok
			m["live_tok_per_sec"] = round1(a.lastTurn.GenPerSec)
			m["per_turn"] = true
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
