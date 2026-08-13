package main

// Task lifecycle, /stop, confirmations and journals
// (ТЗ §4.0, §4.2, §6.3, §6.5; приёмка §10 п. 7, 13, 16, 17, 20, 21, 22, 26, 27, 28, 34).

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Regression from the FIRST LIVE RUN: two engine starts on the same day produced the same
// taskID, so a journal query by id returned rows from BOTH tasks. The id must carry enough of
// the wall clock to survive a restart (§6.5).
func TestTaskIDSurvivesRestart(t *testing.T) {
	first := newTaskID()

	// Simulate a restart INSIDE THE SAME SECOND — new process, counter back to zero. This is
	// the case a timestamp alone does not cover.
	savedSeq, savedProc := taskSeq.Load(), taskIDProc
	taskSeq.Store(0)
	taskIDProc = savedProc + 1
	defer func() { taskSeq.Store(savedSeq); taskIDProc = savedProc }()
	afterRestart := newTaskID()

	if first == afterRestart {
		t.Fatalf("taskID совпал после рестарта: %s — журналы перестанут сходиться по id", first)
	}
	// Still human-sortable: date, then time, then process, then counter.
	if !strings.HasPrefix(first, "task-"+time.Now().Format("20060102")) {
		t.Errorf("taskID должен начинаться с даты: %s", first)
	}
	if len(strings.Split(first, "-")) != 5 { // task | date | time | pid | counter
		t.Errorf("формат taskID изменился: %s", first)
	}
}

// Приёмка №13: a second request in the same chat is answered "занят", not run in parallel.
func TestOneActiveTaskPerChat(t *testing.T) {
	actor := safeActor()
	actor.ChatID = 4242
	first := newTask(actor, "первая")
	if err := registerTask(first); err != nil {
		t.Fatalf("первую задачу не зарегистрировали: %v", err)
	}
	defer func() { unregisterTask(first); first.Close() }()

	second := newTask(actor, "вторая")
	defer second.Close()
	if err := registerTask(second); err == nil {
		t.Fatal("вторая задача в том же чате должна получить «занят»")
	}
	if got := activeTask(actor.ChatID); got != first {
		t.Fatal("активной должна остаться первая задача")
	}
}

// Приёмка №16 + «activeTask снимать по завершении»: a late /stop must not kill the task that
// replaced the finished one.
func TestStopOnlyHitsLiveTask(t *testing.T) {
	actor := safeActor()
	actor.ChatID = 4243

	done := newTask(actor, "уже закончилась")
	_ = registerTask(done)
	done.SetStatus(TaskDone)
	unregisterTask(done)
	done.Close()

	if _, ok := stopActiveTask(actor.ChatID); ok {
		t.Fatal("/stop без активной задачи должен отвечать «нечего останавливать»")
	}

	live := newTask(actor, "живая")
	if err := registerTask(live); err != nil {
		t.Fatalf("после снятия предыдущей регистрация должна проходить: %v", err)
	}
	defer func() { unregisterTask(live); live.Close() }()

	id, ok := stopActiveTask(actor.ChatID)
	if !ok || id != live.ID {
		t.Fatalf("/stop должен остановить живую задачу, получили ok=%v id=%s", ok, id)
	}
	if live.Context().Err() == nil {
		t.Fatal("после /stop контекст задачи должен быть отменён")
	}
	if live.GetStatus() != TaskCancelled {
		t.Fatalf("статус после /stop = %s, ждали cancelled", live.GetStatus())
	}
}

// Приёмка №27: the tool budget is clamped by what remains of the task, never the other way.
func TestToolBudgetClampedByTask(t *testing.T) {
	task := newTask(safeActor(), "x")
	defer task.Close()

	task.Deadline = time.Now().Add(20 * time.Second)
	if b := toolBudget(task); b > 20*time.Second {
		t.Fatalf("бюджет тула %s больше остатка задачи", b)
	}
	task.Deadline = time.Now().Add(-time.Second) // already over
	if b := toolBudget(task); b > time.Second {
		t.Fatalf("после дедлайна бюджет должен схлопнуться, получили %s", b)
	}
}

// Приёмка №27: an expired task says «шла 10 минут, остановил», a cancelled one «остановлено».
func TestInterruptedStatusesDiffer(t *testing.T) {
	cancelled := newTask(safeActor(), "x")
	cancelled.Cancel()
	res, done := checkInterrupted(cancelled)
	if !done || res.Status != TaskCancelled {
		t.Fatalf("отмена: done=%v status=%s", done, res.Status)
	}
	if !strings.Contains(res.Text, "Остановлено") {
		t.Errorf("текст отмены: %q", res.Text)
	}

	expired := newTask(safeActor(), "x")
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	expired.ctx, expired.Cancel = ctx, cancel
	time.Sleep(5 * time.Millisecond)
	res, done = checkInterrupted(expired)
	cancel()
	if !done || res.Status != TaskExpired {
		t.Fatalf("таймаут: done=%v status=%s", done, res.Status)
	}
	if !strings.Contains(res.Text, "10 минут") {
		t.Errorf("текст таймаута должен объяснять причину: %q", res.Text)
	}
}

// Приёмка №26: interrupted work must never reach long-term memory.
func TestInterruptedNotRemembered(t *testing.T) {
	for _, st := range []TaskStatus{TaskCancelled, TaskExpired, TaskLost} {
		if !(agentResult{Status: st}).interrupted() {
			t.Errorf("статус %s должен считаться прерванным (в память не пишем)", st)
		}
	}
	if (agentResult{Status: TaskDone}).interrupted() {
		t.Error("завершённая задача пишется в память")
	}
	if !(agentResult{Status: TaskWaitingConfirm, Waiting: true}).interrupted() {
		t.Error("задача на паузе ещё не результат — в память не пишем")
	}
	if !(agentResult{Busy: true}).interrupted() {
		t.Error("«занят» — не ответ, в память не пишем")
	}
}

// Приёмка №28: a timeout must tell the model the state is UNKNOWN and forbid a blind retry.
func TestToolResultStatuses(t *testing.T) {
	r := classifyToolErr(context.Background(), "частичный вывод", context.DeadlineExceeded)
	if r.Status != StatusTimeout {
		t.Fatalf("статус = %s, ждали timeout", r.Status)
	}
	if !strings.Contains(r.Text, "НЕ повторяй") || !strings.Contains(r.Text, "Состояние неизвестно") {
		t.Fatalf("текст таймаута обязан запрещать слепой повтор: %q", r.Text)
	}
	if !strings.HasPrefix(r.ModelText(), "timeout: ") {
		t.Fatalf("в модель уходит статус строкой: %q", r.ModelText())
	}

	// Cancellation is claimed only when the TASK was really cancelled.
	dead, cancel := context.WithCancel(context.Background())
	cancel()
	if c := classifyToolErr(dead, "", context.Canceled); c.Status != StatusCancelled {
		t.Fatalf("отмена задачи → %s, ждали cancelled", c.Status)
	}
	// From the FIRST LIVE RUN: a stale chromedp page context surfaced as a bare
	// "context canceled" while the task was perfectly alive. Reporting that as `cancelled`
	// made the model tell the user their request had been interrupted — it had not.
	stale := classifyToolErr(context.Background(), "", fmt.Errorf("get_text: %w", context.Canceled))
	if stale.Status != StatusFailed {
		t.Fatalf("живая задача + context canceled из библиотеки → %s, ждали failed", stale.Status)
	}
	if strings.Contains(stale.Text, "прервано пользователем") {
		t.Error("нельзя приписывать пользователю отмену, которой он не делал")
	}
	if ok := classifyToolErr(context.Background(), "готово", nil); ok.Status != StatusOK {
		t.Fatalf("успех → %s", ok.Status)
	}
	// denied / blocked read differently to the model, on purpose (§6.0).
	if !strings.Contains(deniedResult("тест").ModelText(), "denied: ") {
		t.Error("denied должен идти со своим статусом")
	}
	if !strings.Contains(blockedResult("тест").ModelText(), "blocked: ") {
		t.Error("blocked должен идти со своим статусом")
	}
	if !strings.Contains(deferredResult().ModelText(), "deferred: ") {
		t.Error("отложенный вызов должен идти со своим статусом")
	}
}

// Only Text + Status reach the model; artifacts go to the human (§4.2 п. 15).
func TestArtifactsNeverEnterModelContext(t *testing.T) {
	r := okResult("скрин сохранён", []string{`C:\shot.png`})
	if strings.Contains(r.ModelText(), "shot.png") {
		t.Fatal("путь артефакта не должен попадать в контекст модели через ModelText")
	}
	task := newTask(safeActor(), "x")
	defer task.Close()
	task.AddArtifacts(r.Artifacts)
	if got := task.TakeArtifacts(); len(got) != 1 || got[0] != `C:\shot.png` {
		t.Fatalf("артефакт должен уйти в задачу, получили %v", got)
	}
	if len(task.TakeArtifacts()) != 0 {
		t.Fatal("повторный TakeArtifacts должен быть пустым")
	}
}

// Приёмка №22: a new task annuls the hanging confirmation, and the two tasks never share
// artifacts (that was the concrete bug behind Task.Artifacts, §6.3 п. 6).
func TestPendingReplacedByNewTask(t *testing.T) {
	actor := safeActor()
	actor.ChatID = 4244
	old := newTask(actor, "старая")
	old.Artifacts = []string{`C:\old.png`}
	old.Pending = &Pending{
		ID: "p1", TaskID: old.ID, ChatID: actor.ChatID, Channel: channelTelegram,
		Tool: "delete_path", Deadline: time.Now().Add(pendingTimeout),
	}
	savePending(&resumeState{task: old, pending: old.Pending})

	if p := peekPending(actor.ChatID); p == nil || p.Tool != "delete_path" {
		t.Fatal("подтверждение не сохранилось")
	}
	prev := clearPending(actor.ChatID)
	if prev == nil || prev.ID != "p1" {
		t.Fatal("новая задача должна аннулировать старое ожидание и вернуть его")
	}
	if old.GetStatus() != TaskCancelled {
		t.Fatalf("аннулированная задача должна стать cancelled, получили %s", old.GetStatus())
	}
	if peekPending(actor.ChatID) != nil {
		t.Fatal("после аннулирования ожидание должно исчезнуть")
	}
	fresh := newTask(actor, "новая")
	defer fresh.Close()
	if len(fresh.TakeArtifacts()) != 0 {
		t.Fatal("новая задача не должна получить файлы старой")
	}
}

func TestConfirmWordParsing(t *testing.T) {
	yes := []string{"да", "Да!", "ok", "подтверждаю", "+", "давай"}
	no := []string{"нет", "Отмена", "no", "-", "стоп"}
	other := []string{"а что там с эфиром", "да ладно, посмотри почту", ""}
	for _, s := range yes {
		if v, ok := confirmWord(s); !ok || !v {
			t.Errorf("%q должно читаться как «да»", s)
		}
	}
	for _, s := range no {
		if v, ok := confirmWord(s); !ok || v {
			t.Errorf("%q должно читаться как «нет»", s)
		}
	}
	for _, s := range other {
		if _, ok := confirmWord(s); ok {
			t.Errorf("%q — это новая задача, а не ответ на подтверждение", s)
		}
	}
}

// Приёмка №20: after a restart the RAM half is gone — say so, never replay blindly.
func TestStalePendingAfterRestart(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)

	p := Pending{ID: "p-old", ChatID: 77, Tool: "delete_path", Deadline: time.Now().Add(time.Minute)}
	_ = os.MkdirAll("runtime", 0o755)
	body, _ := json.Marshal(p)
	if err := os.WriteFile(pendingStatePath, body, 0o644); err != nil {
		t.Fatal(err)
	}
	reportStalePending()
	if _, err := os.Stat(pendingStatePath); !os.IsNotExist(err) {
		t.Error("протухший pending должен удаляться с диска")
	}
	lost := takeLostPending(77)
	if lost == nil || lost.Tool != "delete_path" {
		t.Fatal("чат должен получить уведомление «задача потерялась»")
	}
	if takeLostPending(77) != nil {
		t.Error("уведомление одноразовое")
	}
}

// Приёмка №34: hands.jsonl and tasks.jsonl join on taskID, and both rotate at 5 MB.
func TestJournalsJoinOnTaskIDAndRotate(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)

	task := newTask(safeActor(), "удали D:\\tmp\\old")
	defer task.Close()
	task.Packs = []string{packFiles}
	task.Step = 2
	task.noteTool("delete_path")
	logHands(task, "delete_path", `{"path":"D:\\tmp\\old"}`,
		askD(ruleOutsideAllow, "удаление вне рабочих каталогов"), "asked", "", "")
	logTask(task, TaskCancelled, 0)

	hands, err := os.ReadFile(handsLogPath)
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := os.ReadFile(tasksLogPath)
	if err != nil {
		t.Fatal(err)
	}
	var hr handsRecord
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(hands))), &hr); err != nil {
		t.Fatal(err)
	}
	var tr taskRecord
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(tasks))), &tr); err != nil {
		t.Fatal(err)
	}
	if hr.TaskID != tr.TaskID || hr.TaskID != task.ID {
		t.Fatalf("по taskID журналы не сходятся: hands=%s tasks=%s task=%s", hr.TaskID, tr.TaskID, task.ID)
	}
	// The journal must answer "почему спросил", not just "спросил" (§6.5).
	if hr.Rule == "" || hr.Reason == "" || hr.Decision != "asked" {
		t.Fatalf("в hands.jsonl нет rule/reason/decision: %+v", hr)
	}
	if tr.Status != string(TaskCancelled) || len(tr.Tools) != 1 || tr.Tools[0] != "delete_path" {
		t.Fatalf("tasks.jsonl не восстанавливает задачу: %+v", tr)
	}

	// Rotation: a file over the cap becomes .1 and a fresh one starts.
	big := make([]byte, jsonlRotateBytes+16)
	for i := range big {
		big[i] = 'x'
	}
	if err := os.WriteFile(handsLogPath, big, 0o644); err != nil {
		t.Fatal(err)
	}
	logHands(task, "read_file", "{}", allowD(), "auto", string(StatusOK), "")
	if _, err := os.Stat(handsLogPath + ".1"); err != nil {
		t.Fatalf("ротации не произошло: %v", err)
	}
	cur, _ := os.ReadFile(handsLogPath)
	if len(cur) > 4096 {
		t.Fatalf("после ротации файл должен начинаться заново, размер %d", len(cur))
	}
}

func TestHandsModeRuntimeStoreOnly(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)

	loadHandsMode()
	if currentHandsMode() != handsModeSafe {
		t.Fatal("по умолчанию режим safe")
	}
	if got := setHandsMode("full", "test"); got != handsModeFull {
		t.Fatalf("setHandsMode(full) = %s", got)
	}
	if _, err := os.Stat(filepath.Join("runtime", "hands_mode.json")); err != nil {
		t.Fatalf("режим должен жить в runtime-store: %v", err)
	}
	// A fresh process reads it back (this is what settings.ini could not do).
	handsMode, handsLoaded = handsModeSafe, false
	loadHandsMode()
	if currentHandsMode() != handsModeFull {
		t.Fatal("режим не пережил перезагрузку состояния")
	}
	// Anything unknown fails closed.
	if got := setHandsMode("что-то странное", "test"); got != handsModeSafe {
		t.Fatalf("неизвестный режим должен падать в safe, получили %s", got)
	}
	setHandsMode("safe", "cleanup")
}

func TestIsOwnerChat(t *testing.T) {
	allow := map[int64]bool{111: true}
	if !isOwnerChat(allow, 111) {
		t.Error("чат из allowlist — владелец")
	}
	// Приёмка №24: a foreign chat must not be able to stop or switch anything.
	if isOwnerChat(allow, 222) {
		t.Error("чужой чат владельцем не является")
	}
	if isOwnerChat(nil, 111) {
		t.Error("без TELEGRAM_ID владельца нет")
	}
	if !isOwnerChat(nil, webChatID) {
		t.Error("Web (loopback + sameOriginGuard) считается владельцем")
	}
}
