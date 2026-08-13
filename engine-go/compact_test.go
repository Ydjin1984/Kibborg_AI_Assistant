package main

// Тесты сжатия контекста (/compact) и учёта занятого окна.

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// seedHistory наполняет историю чата n парами «вопрос — ответ».
func seedHistory(t *testing.T, chatID int64, n int) {
	t.Helper()
	histMu.Lock()
	msgs := make([]chatMsg, 0, n*2)
	for i := 0; i < n; i++ {
		msgs = append(msgs,
			chatMsg{Role: "user", Content: fmt.Sprintf("вопрос номер %d про файл D:\\проект\\файл%d.go", i, i)},
			chatMsg{Role: "assistant", Content: fmt.Sprintf("ответ номер %d, довольно длинный, чтобы токенов было заметно", i)})
	}
	history[chatID] = msgs
	histMu.Unlock()
	t.Cleanup(func() {
		histMu.Lock()
		delete(history, chatID)
		histMu.Unlock()
	})
}

func historyOf(chatID int64) []chatMsg {
	histMu.Lock()
	defer histMu.Unlock()
	return append([]chatMsg{}, history[chatID]...)
}

// Главное свойство /compact: старое ПЕРЕСКАЗЫВАЕТСЯ, а не выбрасывается, и при этом свежий
// хвост диалога остаётся дословно — именно к нему относятся «сохрани это» и «прочитай ещё раз».
func TestCompactSummarisesOldAndKeepsRecent(t *testing.T) {
	chatID := int64(77001)
	seedHistory(t, chatID, 6) // 12 реплик
	fb := newFakeBrain(t, assistantText("- Пользователь чинит D:\\проект\\файл0.go\n- Договорились про порт 8083"))

	before := historyOf(chatID)
	rep := compactChatHistory(Config{BrainPort: fb.port}, chatID)
	if !rep.done() {
		t.Fatalf("сжатие не выполнилось: %s", rep.Note)
	}
	after := historyOf(chatID)

	if len(after) >= len(before) {
		t.Fatalf("история не сжалась: было %d, стало %d", len(before), len(after))
	}
	if rep.AfterTokens >= rep.BeforeTokens {
		t.Errorf("оценка токенов не уменьшилась: %d → %d", rep.BeforeTokens, rep.AfterTokens)
	}
	// Сводка на месте и содержит суть.
	if !strings.Contains(after[1].Content, "файл0.go") {
		t.Errorf("сводка потеряла содержание: %q", after[1].Content)
	}
	// Хвост дословно: последние compactKeepTurns реплик обязаны совпасть посимвольно.
	tailBefore := before[len(before)-compactKeepTurns:]
	tailAfter := after[len(after)-compactKeepTurns:]
	for i := range tailBefore {
		if tailBefore[i].Content != tailAfter[i].Content {
			t.Fatalf("свежий хвост изменился:\nбыло %q\nстало %q", tailBefore[i].Content, tailAfter[i].Content)
		}
	}
	// В отчёте пользователю — числа, а не «готово».
	out := rep.Render()
	for _, want := range []string{"Контекст сжат", "Было:", "Стало:"} {
		if !strings.Contains(out, want) {
			t.Errorf("в отчёте нет %q: %s", want, out)
		}
	}
}

// Сжимать нечего — так и сказать, а не звать модель впустую.
func TestCompactRefusesShortHistory(t *testing.T) {
	chatID := int64(77002)
	seedHistory(t, chatID, 1)
	fb := newFakeBrain(t, assistantText("сводка"))

	rep := compactChatHistory(Config{BrainPort: fb.port}, chatID)
	if rep.done() {
		t.Fatal("на двух репликах сжимать нечего")
	}
	if fb.requestCount() != 0 {
		t.Errorf("модель звать не требовалось, было %d запросов", fb.requestCount())
	}
	if !strings.Contains(rep.Render(), "сжимать нечего") {
		t.Errorf("причина должна быть названа: %s", rep.Render())
	}
}

// Реплики, пришедшие ПОКА модель делала сводку, не должны пропасть: сжатие идёт по снимку,
// а результат склеивается с тем, что появилось позже.
func TestCompactKeepsMessagesArrivedDuringSummary(t *testing.T) {
	chatID := int64(77003)
	seedHistory(t, chatID, 4)
	fb := newFakeBrain(t, assistantText("сводка старого"))

	// Дописываем реплику «во время» сжатия: хук фейкового мозга срабатывает после того, как
	// снимок истории уже сделан, но до того, как сводка вернулась — ровно та гонка, которая
	// в жизни случается, когда пользователь пишет, не дожидаясь ответа.
	fb.onRequest = func(int) {
		histMu.Lock()
		history[chatID] = append(history[chatID],
			chatMsg{Role: "user", Content: "срочное сообщение во время сжатия"},
			chatMsg{Role: "assistant", Content: "принял"})
		histMu.Unlock()
	}
	if rep := compactChatHistory(Config{BrainPort: fb.port}, chatID); !rep.done() {
		t.Fatalf("сжатие не выполнилось: %s", rep.Note)
	}
	joined := ""
	for _, m := range historyOf(chatID) {
		joined += m.Content + "\n"
	}
	if !strings.Contains(joined, "срочное сообщение во время сжатия") {
		t.Fatalf("сообщение, пришедшее во время сжатия, потерялось:\n%s", joined)
	}
}

// Автосжатие включается САМО на пороге — до того, как скользящее окно начнёт терять реплики.
func TestAutoCompactFiresAtThreshold(t *testing.T) {
	chatID := int64(77004)
	fb := newFakeBrain(t, assistantText("автосводка"))
	cfg := Config{BrainPort: fb.port}

	seedHistory(t, chatID, 2) // 4 реплики — далеко до порога
	maybeAutoCompact(cfg, chatID, nil)
	time.Sleep(150 * time.Millisecond)
	if fb.requestCount() != 0 {
		t.Fatalf("ниже порога автосжатие срабатывать не должно (запросов: %d)", fb.requestCount())
	}

	seedHistory(t, chatID, autoCompactAt) // с запасом за порог
	done := make(chan string, 1)
	maybeAutoCompact(cfg, chatID, func(note string) { done <- note })
	select {
	case note := <-done:
		if !strings.Contains(note, "сжал историю сам") {
			t.Errorf("пользователю должно быть сказано, что память изменилась: %q", note)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("автосжатие не сработало на пороге")
	}
	if n := len(historyOf(chatID)); n >= autoCompactAt*2 {
		t.Errorf("история после автосжатия = %d реплик, ожидалось меньше", n)
	}
}

// Окно контекста: «занято» берётся из настоящего prompt_n последнего вызова, а оценка по
// символам живёт отдельным полем и в него не подмешивается.
func TestContextSnapshotSeparatesMeasuredAndEstimated(t *testing.T) {
	chatID := int64(77005)
	seedHistory(t, chatID, 3)
	live.turnDone(GenStats{PromptTokens: 4321, GenTokens: 100, GenPerSec: 42})

	snap := contextSnapshot(Config{BrainPort: 0, CtxSize: 32768}, chatID)
	if snap["total"].(int) != 32768 {
		t.Fatalf("размер окна = %v, ждали 32768 (из конфига, раз сервер молчит)", snap["total"])
	}
	if snap["used"].(int) != 4321 {
		t.Fatalf("занято = %v, ждали 4321 — это prompt_n последнего запроса", snap["used"])
	}
	if pct := snap["pct"].(int); pct != 4321*100/32768 {
		t.Errorf("процент посчитан неверно: %d", pct)
	}
	if snap["history_msgs"].(int) != 6 {
		t.Errorf("реплик в истории = %v, ждали 6", snap["history_msgs"])
	}
	if snap["history_tokens"].(int) <= 0 {
		t.Error("оценка веса истории должна быть положительной")
	}
}

func TestEstimateTokens(t *testing.T) {
	if estimateTokens("") != 0 {
		t.Error("пустая строка — ноль токенов")
	}
	short, long := estimateTokens("привет"), estimateTokens(strings.Repeat("привет ", 100))
	if long <= short {
		t.Error("оценка должна расти с длиной текста")
	}
	// Порядок величины: 700 символов кириллицы — это сотни токенов, а не тысячи и не единицы.
	if long < 100 || long > 700 {
		t.Errorf("оценка %d токенов на 700 символов выглядит неправдоподобной", long)
	}
}
