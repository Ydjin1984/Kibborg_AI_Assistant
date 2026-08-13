package main

// Сжатие контекста (/compact) и учёт того, сколько контекста вообще занято.
//
// До этого «переполнение» решалось молча и грубо: recordHistory держит скользящее окно в
// maxHistory сообщений и просто ВЫБРАСЫВАЕТ самое старое. Пользователь этого не видит — он
// видит только, что бот «забыл» начало разговора. /compact заменяет выбрасывание на пересказ:
// старая часть диалога уходит в одну сводку, свежие реплики остаются как есть.
//
// Числа тут двух сортов и не смешиваются:
//   - сколько контекста занято ПРЯМО СЕЙЧАС — это prompt_n последнего вызова, его считает
//     llama.cpp, и он точный;
//   - сколько весит история чата — это оценка по символам, и она честно названа оценкой.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// compactCommands — слэш-ярлыки сжатия в обоих каналах.
var compactCommands = []string{"/compact", "/сжать", "/сжатие", "/compress"}

const (
	// compactKeepTurns — сколько последних реплик остаётся дословно. Свежий хвост диалога
	// нужен буквально: именно к нему относятся «сохрани это», «прочитай подробнее».
	compactKeepTurns = 4
	// compactMinMessages — ниже этого сжимать нечего, и честнее так и сказать.
	compactMinMessages = 6
	// compactSummaryChars — потолок сводки. Сводка длиннее исходника бессмысленна.
	compactSummaryChars = 2000
	// autoCompactAt — на этом размере истории сжатие запускается само, ДО того как окно
	// начнёт терять старые реплики (maxHistory = 12).
	autoCompactAt = maxHistory
)

// compactionReport is what the user is told after a compaction.
type compactionReport struct {
	Before       int    `json:"before_msgs"`
	After        int    `json:"after_msgs"`
	BeforeTokens int    `json:"before_tokens"`
	AfterTokens  int    `json:"after_tokens"`
	Summary      string `json:"summary"`
	Note         string `json:"note,omitempty"` // причина, если сжимать было нечего
}

func (r compactionReport) done() bool { return r.Note == "" }

// Render is the message both channels show.
func (r compactionReport) Render() string {
	if !r.done() {
		return "🗜 " + r.Note
	}
	saved := r.BeforeTokens - r.AfterTokens
	pct := 0
	if r.BeforeTokens > 0 {
		pct = saved * 100 / r.BeforeTokens
	}
	return fmt.Sprintf("🗜 **Контекст сжат**\n"+
		"Было: %d реплик ≈ %d токенов\nСтало: %d реплик ≈ %d токенов (−%d%%)\n\n"+
		"**Что осталось в памяти:**\n%s",
		r.Before, r.BeforeTokens, r.After, r.AfterTokens, pct, r.Summary)
}

// compactChatHistory summarises the older half of a chat's history in place.
func compactChatHistory(cfg Config, chatID int64) compactionReport {
	histMu.Lock()
	prior := append([]chatMsg{}, history[chatID]...)
	histMu.Unlock()

	if len(prior) < compactMinMessages {
		return compactionReport{
			Before: len(prior), After: len(prior),
			Note: fmt.Sprintf("сжимать нечего: в контексте всего %d реплик.", len(prior)),
		}
	}
	if !brainReady(cfg.BrainPort) {
		return compactionReport{Note: "модель ещё грузится — сжать не могу, повтори чуть позже."}
	}

	keep := compactKeepTurns
	if keep >= len(prior) {
		keep = len(prior) / 2
	}
	head, tail := prior[:len(prior)-keep], prior[len(prior)-keep:]

	summary, err := summariseDialog(cfg, head)
	if err != nil {
		log.Printf("[COMPACT] чат %d: %v", chatID, err)
		return compactionReport{Note: "не удалось сжать: " + err.Error()}
	}

	compacted := append([]chatMsg{
		{Role: "user", Content: "[сжатая история предыдущего разговора]"},
		{Role: "assistant", Content: summary},
	}, tail...)

	histMu.Lock()
	// За время вызова модели пользователь мог дописать реплик — приклеиваем всё, что появилось
	// после снимка, иначе /compact молча съел бы свежее сообщение.
	if cur := history[chatID]; len(cur) > len(prior) {
		compacted = append(compacted, cur[len(prior):]...)
	}
	history[chatID] = compacted
	markHistoryDirty()
	histMu.Unlock()

	rep := compactionReport{
		Before:       len(prior),
		After:        len(compacted),
		BeforeTokens: estimateMsgTokens(prior),
		AfterTokens:  estimateMsgTokens(compacted),
		Summary:      summary,
	}
	log.Printf("[COMPACT] чат %d: %d → %d реплик, ≈%d → ≈%d токенов",
		chatID, rep.Before, rep.After, rep.BeforeTokens, rep.AfterTokens)
	return rep
}

// summariseDialog asks the model for a factual digest of the older turns.
func summariseDialog(cfg Config, msgs []chatMsg) (string, error) {
	var b strings.Builder
	for _, m := range msgs {
		who := "Пользователь"
		if m.Role == "assistant" {
			who = "Ты"
		}
		b.WriteString(who + ": " + capAgentText(m.Content, 1200) + "\n")
	}
	sys := "Ты сжимаешь историю диалога для самого себя — это твоя память, а не отчёт человеку.\n" +
		"Сохрани: задачи и цели пользователя, принятые решения, факты и числа с источниками, " +
		"пути к файлам, имена, договорённости, незакрытые вопросы.\n" +
		"Выброси: приветствия, повторы, воду, свои же рассуждения.\n" +
		"Ничего не выдумывай и не додумывай. Пиши по-русски, сжатыми пунктами, без вступления. " +
		"Уложись в 1500 символов."
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	out, _, err := llmChatTools(ctx, cfg.BrainPort, []map[string]any{
		{"role": "system", "content": sys},
		{"role": "user", "content": "Сожми этот диалог:\n\n" + b.String()},
	}, nil, 0.2)
	if err != nil {
		return "", err
	}
	summary := strings.TrimSpace(stripThink(out.Content))
	if summary == "" {
		return "", fmt.Errorf("модель вернула пустую сводку")
	}
	return capAgentText(summary, compactSummaryChars), nil
}

// ===== автосжатие =====

// compactingChats guards against two compactions of the same chat at once (the model call is
// slow, and a second one would summarise a half-rewritten history).
var compactingChats sync.Map

// maybeAutoCompact сжимает историю САМ, когда окно вот-вот начнёт терять старые реплики.
// Работает в фоне: пользователь не должен ждать сжатия ради ответа на свой вопрос.
// notify получает готовую сводку — молчаливое изменение памяти пользователь бы не заметил.
func maybeAutoCompact(cfg Config, chatID int64, notify func(string)) {
	histMu.Lock()
	n := len(history[chatID])
	histMu.Unlock()
	if n < autoCompactAt {
		return
	}
	if _, busy := compactingChats.LoadOrStore(chatID, true); busy {
		return
	}
	go func() {
		defer compactingChats.Delete(chatID)
		rep := compactChatHistory(cfg, chatID)
		if !rep.done() {
			return
		}
		if notify != nil {
			notify(fmt.Sprintf("🗜 Контекст подошёл к пределу — сжал историю сам: %d → %d реплик "+
				"(≈%d → ≈%d токенов). Ничего не потеряно, старое пересказано в сводку.",
				rep.Before, rep.After, rep.BeforeTokens, rep.AfterTokens))
		}
	}()
}

// ===== учёт контекста =====

// estimateTokens — оценка, а не измерение. Для русского текста в токенизаторе Qwen выходит
// примерно 2.5 символа на токен; точное число знает только llama.cpp, и оно приезжает в
// prompt_n после запроса. Оценка нужна там, где запроса ещё не было.
func estimateTokens(s string) int {
	if s == "" {
		return 0
	}
	return utf8.RuneCountInString(s)*10/25 + 1
}

func estimateMsgTokens(msgs []chatMsg) int {
	n := 0
	for _, m := range msgs {
		n += estimateTokens(m.Content) + 4 // служебные токены роли и разделителей
	}
	return n
}

var (
	ctxSizeMu     sync.Mutex
	ctxSizeCached int
	ctxSizePort   int // кэш привязан к порту: сменился мозг — сменилось и окно
	ctxSizeAt     time.Time
)

// brainCtxSize returns the context window llama-server actually runs with.
//
// Берётся у сервера, а не из settings.ini: значение в конфиге — это то, что мы ПОПРОСИЛИ, а
// llama-server мог урезать его под VRAM. Показывать пользователю запрошенное вместо реального
// значит рисовать запас, которого нет.
func brainCtxSize(cfg Config) int {
	ctxSizeMu.Lock()
	if ctxSizeCached > 0 && ctxSizePort == cfg.BrainPort && time.Since(ctxSizeAt) < 5*time.Minute {
		defer ctxSizeMu.Unlock()
		return ctxSizeCached
	}
	ctxSizeMu.Unlock()

	n := fetchCtxSize(cfg.BrainPort)
	if n <= 0 {
		n = cfg.CtxSize // сервер молчит — показываем хотя бы запрошенное
	}
	if n > 0 {
		ctxSizeMu.Lock()
		ctxSizeCached, ctxSizePort, ctxSizeAt = n, cfg.BrainPort, time.Now()
		ctxSizeMu.Unlock()
	}
	return n
}

func fetchCtxSize(port int) int {
	c := &http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d/props", port))
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0
	}
	var props struct {
		NCtx     int `json:"n_ctx"`
		Defaults struct {
			NCtx int `json:"n_ctx"`
		} `json:"default_generation_settings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&props); err != nil {
		return 0
	}
	if props.Defaults.NCtx > 0 {
		return props.Defaults.NCtx
	}
	return props.NCtx
}

// contextSnapshot is the block the Web panel renders as the context meter.
func contextSnapshot(cfg Config, chatID int64) map[string]any {
	total := brainCtxSize(cfg)
	used := live.contextTokens()

	histMu.Lock()
	msgs := len(history[chatID])
	histTokens := estimateMsgTokens(history[chatID])
	histMu.Unlock()

	m := map[string]any{
		"total":           total,
		"used":            used,
		"history_msgs":    msgs,
		"history_tokens":  histTokens,
		"auto_compact_at": autoCompactAt,
	}
	if total > 0 && used > 0 {
		m["pct"] = used * 100 / total
		m["free"] = total - used
	}
	return m
}

// handleCompactCommand — общий обработчик /compact для обоих каналов.
func handleCompactCommand(cfg Config, chatID int64) string {
	if _, busy := compactingChats.LoadOrStore(chatID, true); busy {
		return "🗜 Уже сжимаю контекст — подожди несколько секунд."
	}
	defer compactingChats.Delete(chatID)
	return compactChatHistory(cfg, chatID).Render()
}
