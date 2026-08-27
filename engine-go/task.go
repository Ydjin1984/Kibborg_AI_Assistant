package main

// Task as an object (ТЗ §4.0). Before this, agent state was smeared across three maps keyed
// by chatID (cancel func, pending confirmation, artifact buffer) — and chatID is not a task:
// a late /stop could cancel the NEXT task, and a paused task's files could be handed to a
// neighbour. One *Task per chat holds all of it, and taskID ties every journal line together.

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// TaskStatus is the lifecycle of one user request. Six states, transitions described in §6.3 —
// a formal FSM table would add a layer without adding reliability (§14).
type TaskStatus string

const (
	TaskRunning        TaskStatus = "running"
	TaskWaitingConfirm TaskStatus = "waiting_confirm"
	TaskDone           TaskStatus = "done"
	TaskCancelled      TaskStatus = "cancelled"
	TaskFailed         TaskStatus = "failed"
	TaskExpired        TaskStatus = "expired"
	TaskLost           TaskStatus = "lost"
)

// TaskTimeout caps a whole task, dispatcher → final answer. Waiting for a confirmation is NOT
// counted against it (pending has its own 5 minutes, §6.3).
//
// Security audits and multi-tool agent runs routinely exceed 10 minutes (nuclei, crawls,
// confirmations). Context is compacted when full — the job must finish, not die on a clock.
// /stop remains the hard abort. 24h is a safety net against runaway loops, not a UX limit.
const TaskTimeout = 24 * time.Hour

// Channel names used in Actor / journals.
const (
	channelTelegram = "telegram"
	channelWeb      = "web"
)

// Actor is who is asking. The guard needs all of it: mode decides ask vs allow, channel+chatID
// decide where a confirmation question goes, isOwner gates the full-hands switch (§6.0).
type Actor struct {
	Mode    string `json:"mode"` // safe | full
	Channel string `json:"channel"`
	ChatID  int64  `json:"chatID"`
	IsOwner bool   `json:"isOwner"`
}

// CompactResult is one already-executed step, small enough to survive in RAM across a pending
// confirmation (§6.3 п. 4) — the same idea as compactToolMessages, not a live []msg.
type CompactResult struct {
	Tool   string `json:"tool"`
	Args   string `json:"args"`
	Status string `json:"status"`
	Text   string `json:"text"`
}

// Task is the unit of work: one user request from dispatcher to final answer.
type Task struct {
	ID      string
	ChatID  int64
	Channel string
	Actor   Actor

	Input string
	Packs []string
	Plan  []string

	Status TaskStatus
	Step   int

	Steps     []CompactResult // compacted results, for resuming after a confirmation
	Artifacts []string        // this task's files, not the shared session buffer
	// FailCount is keyed by FINGERPRINT (tool + normalized args): delete_path(A) and
	// delete_path(B) are different intentions and must not silence each other (§4.2).
	FailCount map[string]int
	// ToolFail is keyed by tool name: a tool that never works at all should end the loop.
	ToolFail map[string]int
	// SeenOK — успешные вызовы по fingerprint. Без этого модель крутит один и тот же
	// probe_url до исчерпания step budget, а summarize() получает обрезанный мусор.
	SeenOK map[string]int
	// ToolOK — сколько раз tool-имя успешно отработало в этой задаче (даже с разными args).
	// Живой пентест: 59× probe_url / 10× run_command — квота режет спам по семейству.
	ToolOK map[string]int

	Pending     *Pending
	Deadline    time.Time
	Cancel      context.CancelFunc
	ctx         context.Context
	Escalations int
	ToolsUsed   []string
	// ProseCalls counts tool calls recovered from the model's prose (prose_calls.go). Logged
	// so the rate stays visible: if it climbs, the chat template or the model is the problem,
	// not the agent.
	ProseCalls int
	// FailedActions — ДЕЙСТВИЯ (не чтения), которые не выполнились. Нужны затем, что модель
	// умеет не заметить `failed` в ответе инструмента: на живом прогоне клик мышью и попытка
	// вывести окно вперёд оба провалились, а итоговый ответ был «Отлично! Чат уже открыт».
	// Факт провала фиксирует Go и приписывает к ответу сам — спорить тут не с чем.
	FailedActions []string

	StartedAt     time.Time
	DispatcherMs  int64
	DispatcherRaw string
	// FirstPromptMs/Tokens: prompt_ms of the first EXECUTOR turn (§3.2). Measuring the
	// dispatcher's prompt length instead would answer the wrong question — the cost that
	// matters is re-processing the executor's system prompt once per task.
	FirstPromptMs     float64
	FirstPromptTokens int

	mu sync.Mutex
}

// Thresholds from §4.2: same action twice → tell the model it already tried; same tool four
// times → leave the loop honestly instead of burning all 12 steps.
const (
	fingerprintFailLimit = 2
	toolFailLimit        = 4
	// toolOKQuota — мягкий потолок УСПЕШНЫХ вызовов одного имени за задачу (пентест-спам).
	toolOKQuota = 4
)

var taskSeq atomic.Uint64

// taskIDProc distinguishes one engine run from the next. It is a variable, not a call to
// os.Getpid() inline, so a test can simulate a restart.
var taskIDProc = os.Getpid()

// newTaskID is human-sortable and unique ACROSS RESTARTS:
//
//	task-20260813-001927-6720-000007
//	     date     time   pid  counter
//
// The counter alone is not enough — it lives in the process, so after a restart numbering
// begins again and `task-20260813-000001` names two different tasks. That silently breaks the
// one thing the journals exist for (§6.5: «по taskID поднимается вся задача целиком»), and it
// did break on the first live run: two engine starts on the same day produced colliding ids
// and a query by id returned rows from both tasks.
//
// Wall-clock time alone is not enough either — a restart inside the same second collides
// again, which is not a hypothetical: the regression test reproduces it deterministically.
// The pid is the part the OS guarantees is unique among live processes.
func newTaskID() string {
	return fmt.Sprintf("task-%s-%d-%06d", time.Now().Format("20060102-150405"), taskIDProc, taskSeq.Add(1))
}

// newTask creates a running task with its own cancellable, deadline-bound context. The caller
// must call task.Close() (defer) so the context is released and the chat slot freed.
func newTask(actor Actor, input string) *Task {
	ctx, cancel := context.WithTimeout(context.Background(), TaskTimeout)
	return &Task{
		ID:        newTaskID(),
		ChatID:    actor.ChatID,
		Channel:   actor.Channel,
		Actor:     actor,
		Input:     input,
		Status:    TaskRunning,
		FailCount: map[string]int{},
		ToolFail:  map[string]int{},
		SeenOK:    map[string]int{},
		ToolOK:    map[string]int{},
		Deadline:  time.Now().Add(TaskTimeout),
		Cancel:    cancel,
		ctx:       ctx,
		StartedAt: time.Now(),
	}
}

// Context is the task context every tool call, child process and LLM request runs under.
func (t *Task) Context() context.Context {
	if t == nil || t.ctx == nil {
		return context.Background()
	}
	return t.ctx
}

// armWaitContext replaces the work deadline with a cancel-only context while parked on a
// human confirmation. Waiting must not burn TaskTimeout (§6.3 / pending.go).
func (t *Task) armWaitContext() {
	if t == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.mu.Lock()
	t.ctx = ctx
	t.Cancel = cancel
	t.Deadline = time.Now().Add(pendingTimeout)
	t.mu.Unlock()
}

// armWorkContext starts a fresh TaskTimeout budget after the user answers a confirmation.
func (t *Task) armWorkContext() {
	if t == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), TaskTimeout)
	t.mu.Lock()
	t.ctx = ctx
	t.Cancel = cancel
	t.Deadline = time.Now().Add(TaskTimeout)
	t.mu.Unlock()
}

// SetStatus records an intermediate or terminal state under the lock.
func (t *Task) SetStatus(s TaskStatus) {
	t.mu.Lock()
	t.Status = s
	t.mu.Unlock()
}

// GetStatus reads the status under the lock.
func (t *Task) GetStatus() TaskStatus {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.Status
}

// AddArtifacts drains the session buffer into THIS task after every step (§6.3 п. 6): with a
// pending confirmation in flight, a neighbouring task must never collect our files.
// Дубликаты отбрасываем: download_url/write_security_report раньше клали путь и в
// okResult.Artifacts, и через t.AddArtifacts — в панели один файл светился дважды.
func (t *Task) AddArtifacts(paths []string) {
	if len(paths) == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	seen := make(map[string]bool, len(t.Artifacts)+len(paths))
	for _, p := range t.Artifacts {
		seen[p] = true
	}
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		t.Artifacts = append(t.Artifacts, p)
	}
}

// TakeArtifacts returns and clears the task's own artifacts.
func (t *Task) TakeArtifacts() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := t.Artifacts
	t.Artifacts = nil
	return out
}

// AddStep appends a compacted result used to resume after a confirmation.
func (t *Task) AddStep(r CompactResult) {
	t.mu.Lock()
	t.Steps = append(t.Steps, r)
	t.mu.Unlock()
}

// noteTool records the tool name for tasks.jsonl (deduped, order preserved).
func (t *Task) noteTool(name string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, n := range t.ToolsUsed {
		if n == name {
			return
		}
	}
	t.ToolsUsed = append(t.ToolsUsed, name)
}

// actionTools меняют мир за пределами процесса: их провал нельзя пересказать как успех.
// Чтения сюда не входят — «не нашёл страницу» модель и так сообщает нормально.
var actionTools = map[string]bool{
	"mouse_action": true, "type_keyboard": true, "press_keys": true, "focus_window": true,
	"launch_app": true, "kill_process": true,
	"write_file": true, "delete_path": true, "mkdir": true, "run_command": true,
	"click_element": true, "type_text": true, "submit_form": true, "upload_file": true,
	"convert_media": true,
}

// noteFailedAction records an action that did not happen, for the closing notice.
func (t *Task) noteFailedAction(tool, reason string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.FailedActions) >= 6 {
		return
	}
	t.FailedActions = append(t.FailedActions, tool+": "+scrubSecrets(capAgentText(reason, 160)))
}

// failedActionsNotice is the line the human sees when the answer claims more than happened.
func (t *Task) failedActionsNotice() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.FailedActions) == 0 {
		return ""
	}
	return "⚠️ Не все действия выполнились (это факт из журнала, а не мнение модели):\n- " +
		strings.Join(t.FailedActions, "\n- ")
}

// bumpFail counts a failure on both axes and reports which limit (if any) was hit.
func (t *Task) bumpFail(fingerprint, tool string) (repeated bool, deadTool bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.FailCount[fingerprint]++
	t.ToolFail[tool]++
	return t.FailCount[fingerprint] >= fingerprintFailLimit, t.ToolFail[tool] >= toolFailLimit
}

// seenOKCount returns how many times this exact call already succeeded in the task.
func (t *Task) seenOKCount(fingerprint string) int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.SeenOK == nil {
		return 0
	}
	return t.SeenOK[fingerprint]
}

// bumpSeenOK records a successful call; returns the new count.
func (t *Task) bumpSeenOK(fingerprint string) int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.SeenOK == nil {
		t.SeenOK = map[string]int{}
	}
	t.SeenOK[fingerprint]++
	return t.SeenOK[fingerprint]
}

func (t *Task) toolOKCount(name string) int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ToolOK == nil {
		return 0
	}
	return t.ToolOK[name]
}

func (t *Task) bumpToolOK(name string) int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ToolOK == nil {
		t.ToolOK = map[string]int{}
	}
	t.ToolOK[name]++
	return t.ToolOK[name]
}

// toolHasOKQuota — семейства, которые на живых прогонах уходили в спам.
func toolHasOKQuota(name string) bool {
	switch name {
	case "probe_url", "http_get", "read_url", "download_url",
		"run_command", "list_dir", "web_search", "semantic_search":
		return true
	default:
		return false
	}
}

// Close cancels the task context. Safe to call twice.
func (t *Task) Close() {
	if t != nil && t.Cancel != nil {
		t.Cancel()
	}
}

// Expired reports whether the task ran into TaskTimeout (as opposed to being cancelled by
// /stop) — the two get different words to the user (§4.2 vs §4.2 «/stop»).
func (t *Task) Expired() bool {
	return t != nil && t.ctx != nil && t.ctx.Err() == context.DeadlineExceeded
}

// remainingBudget is how much of TaskTimeout is left; tool timeouts are clamped to it so a
// `run_command timeout_sec=600` started on minute 9 gets the remainder, not 600 s (§4.2).
func (t *Task) remainingBudget() time.Duration {
	if t == nil {
		return TaskTimeout
	}
	d := time.Until(t.Deadline)
	if d < 0 {
		return 0
	}
	return d
}

// ===== registry: chatID → active task (§4.0) =====
//
// Invariant: ONE active task per chat (the agent is single-threaded on browserTaskMu anyway).
// A second request answers "занят, в очереди". This makes the races an external review worried
// about impossible by construction rather than by carefulness.

var (
	activeMu    sync.Mutex
	activeTasks = map[int64]*Task{}
)

// registerTask publishes the task for /stop, /hands and confirmations. It fails when the chat
// already has a live task.
func registerTask(t *Task) error {
	activeMu.Lock()
	defer activeMu.Unlock()
	if cur, ok := activeTasks[t.ChatID]; ok && cur.GetStatus() == TaskRunning {
		return fmt.Errorf("занят: задача %s ещё выполняется", cur.ID)
	}
	activeTasks[t.ChatID] = t
	return nil
}

// unregisterTask removes the task — but only if it is still the current one, so a late
// finisher can never unpublish the task that replaced it.
func unregisterTask(t *Task) {
	activeMu.Lock()
	defer activeMu.Unlock()
	if cur, ok := activeTasks[t.ChatID]; ok && cur == t {
		delete(activeTasks, t.ChatID)
	}
}

// activeTask returns the chat's current task (nil if idle).
func activeTask(chatID int64) *Task {
	activeMu.Lock()
	defer activeMu.Unlock()
	return activeTasks[chatID]
}

// stopActiveTask cancels the chat's running task. Reports false when there was nothing to
// stop, so /stop can answer honestly instead of pretending.
func stopActiveTask(chatID int64) (string, bool) {
	activeMu.Lock()
	t := activeTasks[chatID]
	activeMu.Unlock()
	if t == nil {
		return "", false
	}
	if st := t.GetStatus(); st != TaskRunning && st != TaskWaitingConfirm {
		return "", false
	}
	t.SetStatus(TaskCancelled)
	t.Close()
	clearPending(chatID)
	// A PARKED task has no goroutine left to run its deferred cleanup, so unregister here —
	// otherwise the chat would look busy until the next task overwrote the slot.
	unregisterTask(t)
	return t.ID, true
}

// ===== journal =====

// logTask writes the task's summary line to tasks.jsonl (§6.5).
func logTask(t *Task, status TaskStatus, artifacts int) {
	appendJSONL(tasksLogPath, taskRecord{
		TS:                time.Now().Format(time.RFC3339),
		TaskID:            t.ID,
		Channel:           t.Channel,
		ChatID:            t.ChatID,
		InputCap:          capAgentText(t.Input, 300),
		Packs:             t.Packs,
		Escalations:       t.Escalations,
		Steps:             t.Step,
		Tools:             t.ToolsUsed,
		Status:            string(status),
		DispatcherMs:      t.DispatcherMs,
		TotalMs:           time.Since(t.StartedAt).Milliseconds(),
		FirstPromptMs:     int64(t.FirstPromptMs),
		FirstPromptTokens: t.FirstPromptTokens,
		ProseCalls:        t.ProseCalls,
		Artifacts:         artifacts,
		DispatcherRaw:     capAgentText(t.DispatcherRaw, 600),
	})
}

// logHands writes one guard decision to hands.jsonl (§6.5).
func logHands(t *Task, name, argsJSON string, d Decision, decision, status, exit string) {
	appendJSONL(handsLogPath, handsRecord{
		TS:       time.Now().Format(time.RFC3339),
		TaskID:   t.ID,
		Channel:  t.Channel,
		ChatID:   t.ChatID,
		Mode:     t.Actor.Mode,
		Tool:     name,
		ArgsCap:  capAgentText(argsJSON, 300),
		Decision: decision,
		Risk:     d.Risk,
		Rule:     d.Rule,
		Reason:   d.Reason,
		Status:   status,
		Exit:     capAgentText(exit, 200),
	})
}

// statusWord turns a terminal status into the sentence the user actually sees (§8).
func statusWord(s TaskStatus) string {
	switch s {
	case TaskCancelled:
		return "⏹ Остановлено."
	case TaskExpired:
		return "⏱ Задача шла 10 минут — остановил. Сформулируй короче или разбей на шаги."
	case TaskLost:
		return "🕳 Задача потерялась (перезапуск) — повтори запрос."
	case TaskFailed:
		return "❌ Задача завершилась ошибкой."
	default:
		return ""
	}
}

// describePlan renders the dispatcher's plan as the status ribbon shown to the user (§4.1:
// plan must reach the UI, otherwise it is a dead field).
func describePlan(plan []string) string {
	if len(plan) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("🧭 План:")
	for i, p := range plan {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		fmt.Fprintf(&b, "\n%d. %s", i+1, capAgentText(p, 120))
	}
	return b.String()
}
