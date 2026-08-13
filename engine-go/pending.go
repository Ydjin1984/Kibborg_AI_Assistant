package main

// Non-blocking confirmation (ТЗ §6.3). Holding browserTaskMu while waiting for "да" is a
// deadlock: /hands, /stop and every other chat would freeze behind a question nobody can
// answer. So the task PAUSES instead: it releases the mutex, keeps its compacted steps in
// RAM, writes only metadata to disk, and returns to the user.
//
// Resuming continues FROM those compacted steps and executes ONLY the confirmed tool — not a
// replay. run_command is not idempotent, a downloaded video is downloaded, a written file is
// written; replaying "everything up to the question" would do all of it twice.

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"kibborg/engine/browser"
)

// pendingTimeout is how long a question waits before it is dropped as refused (§6.3 п. 9).
// It is NOT counted against TaskTimeout — waiting on a human is not the agent burning time.
const pendingTimeout = 5 * time.Minute

const pendingStatePath = "runtime/pending.json"

// Pending is the metadata of one paused tool call. This is the ONLY part that hits the disk:
// enough to tell the user what was pending after a restart, not enough to blindly replay it.
type Pending struct {
	ID         string         `json:"id"`
	TaskID     string         `json:"taskID"`
	ChatID     int64          `json:"chatID"`
	Channel    string         `json:"channel"`
	Tool       string         `json:"tool"`
	Args       map[string]any `json:"args"`
	ToolCallID string         `json:"tool_call_id"`
	Reason     string         `json:"reason"`
	Rule       string         `json:"rule"`
	Deadline   time.Time      `json:"deadline"`
	CreatedAt  time.Time      `json:"created_at"`
}

// resumeState is the RAM half: the compacted conversation the task will continue from.
// Deliberately not serialized (§11: "сериализация полного msgs на диск" is out of scope) —
// after a process restart the honest answer is «задача потерялась, повтори» (§6.3 п. 10).
type resumeState struct {
	task    *Task
	cfg     Config
	pending *Pending
	msgs    []map[string]any // compacted history INCLUDING the assistant tool_calls message
	sys     string
	packs   []string
	tools   []browser.ToolSpec
	didRead bool
	didSrch bool
}

var (
	pendingMu   sync.Mutex
	pendingByID = map[int64]*resumeState{} // chatID → paused task (one per chat, §6.3 п. 7)
)

// newPendingID is unique per chat and monotonic enough to read in a log.
func newPendingID(taskID string) string {
	return taskID + "-ask-" + time.Now().Format("150405")
}

// savePending records the paused task. It returns the previous pending (if any) so the caller
// can tell the user which question it just cancelled (§6.3 п. 7).
func savePending(rs *resumeState) *Pending {
	pendingMu.Lock()
	prev := pendingByID[rs.task.ChatID]
	pendingByID[rs.task.ChatID] = rs
	pendingMu.Unlock()

	persistPending(rs.pending)
	if prev != nil {
		return prev.pending
	}
	return nil
}

// takePending removes and returns the chat's paused task.
func takePending(chatID int64) *resumeState {
	pendingMu.Lock()
	rs := pendingByID[chatID]
	delete(pendingByID, chatID)
	pendingMu.Unlock()
	if rs != nil {
		clearPersistedPending(rs.pending.ChatID)
	}
	return rs
}

// peekPending reports the chat's paused question without consuming it.
func peekPending(chatID int64) *Pending {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	if rs := pendingByID[chatID]; rs != nil {
		return rs.pending
	}
	return nil
}

// clearPending drops a pending question (used by /stop and by a new task taking over).
func clearPending(chatID int64) *Pending {
	rs := takePending(chatID)
	if rs == nil {
		return nil
	}
	rs.task.SetStatus(TaskCancelled)
	rs.task.Close()
	return rs.pending
}

// pendingExpired reports whether the question timed out.
func (p *Pending) expired() bool {
	return p != nil && time.Now().After(p.Deadline)
}

// question is what the user is actually asked.
func (p *Pending) question() string {
	var b strings.Builder
	b.WriteString("⏸ Жду подтверждения.\n")
	b.WriteString("Действие: `" + p.Tool + "`")
	if args := renderPendingArgs(p.Args); args != "" {
		b.WriteString(" " + args)
	}
	if p.Reason != "" {
		b.WriteString("\nПричина: " + p.Reason)
	}
	b.WriteString("\n\nОтветь **да** — выполню только это действие, уже сделанные шаги не повторяю. " +
		"**нет** — откажусь. Через 5 минут вопрос снимается сам.")
	return b.String()
}

// renderPendingArgs shows the arguments as plain `key=value` pairs. Raw JSON would escape
// Windows paths into `D:\\tmp\\old`, and the user is being asked to approve a real path.
func renderPendingArgs(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		var val string
		switch v := args[k].(type) {
		case string:
			val = v
		default:
			raw, _ := json.Marshal(v)
			val = string(raw)
		}
		parts = append(parts, k+"="+capAgentText(val, 120))
	}
	return capAgentText(strings.Join(parts, " "), 300)
}

// ===== disk half (metadata only) =====

func persistPending(p *Pending) {
	if p == nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(pendingStatePath), 0o755); err != nil {
		log.Printf("[PENDING] mkdir: %v", err)
		return
	}
	body, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return
	}
	if err := os.WriteFile(pendingStatePath, body, 0o644); err != nil {
		log.Printf("[PENDING] сохранение: %v", err)
	}
}

func clearPersistedPending(int64) {
	_ = os.Remove(pendingStatePath)
}

// stalePendingOnDisk returns a pending recorded before this process started — RAM is empty,
// so it cannot be resumed. The user gets the truth, not a blind replay (§6.3 п. 10).
func stalePendingOnDisk() *Pending {
	data, err := os.ReadFile(pendingStatePath)
	if err != nil {
		return nil
	}
	var p Pending
	if err := json.Unmarshal(data, &p); err != nil {
		return nil
	}
	return &p
}

// reportStalePending logs (and clears) a confirmation that died with the previous process.
func reportStalePending() {
	p := stalePendingOnDisk()
	if p == nil {
		return
	}
	log.Printf("[PENDING] найдено протухшее подтверждение %s (%s) — задача потерялась при рестарте", p.ID, p.Tool)
	_ = os.Remove(pendingStatePath)
	lostPendingMu.Lock()
	lostPending[p.ChatID] = p
	lostPendingMu.Unlock()
}

// lostPending remembers "your task died in a restart" per chat, so the NEXT message in that
// chat gets an honest «задача потерялась, повтори» instead of silence.
var (
	lostPendingMu sync.Mutex
	lostPending   = map[int64]*Pending{}
)

// takeLostPending consumes the restart notice for a chat.
func takeLostPending(chatID int64) *Pending {
	lostPendingMu.Lock()
	defer lostPendingMu.Unlock()
	p := lostPending[chatID]
	if p != nil {
		delete(lostPending, chatID)
	}
	return p
}

// ===== answers =====

// confirmWord classifies a user reply to a pending question. ok=false → not an answer at all
// (it is a new task, and a new task cancels the pending one, §6.3 п. 7).
func confirmWord(text string) (yes bool, ok bool) {
	t := strings.ToLower(strings.TrimSpace(text))
	t = strings.Trim(t, " .!,")
	switch t {
	case "да", "ага", "давай", "подтверждаю", "yes", "y", "ok", "окей", "ок", "го", "делай", "+":
		return true, true
	case "нет", "не", "отмена", "no", "n", "стоп", "не надо", "отставить", "-":
		return false, true
	}
	return false, false
}

// expirePendingLoop drops questions nobody answered within pendingTimeout. One goroutine for
// the whole process; a paused task holds no locks, so this only has to flip its status.
func expirePendingLoop(notify func(chatID int64, channel, text string)) {
	for {
		time.Sleep(30 * time.Second)
		var expired []*resumeState
		pendingMu.Lock()
		for chatID, rs := range pendingByID {
			if rs.pending.expired() {
				expired = append(expired, rs)
				delete(pendingByID, chatID)
			}
		}
		pendingMu.Unlock()
		for _, rs := range expired {
			clearPersistedPending(rs.pending.ChatID)
			rs.task.SetStatus(TaskCancelled)
			rs.task.Close()
			unregisterTask(rs.task) // the parked goroutine is gone; free the chat slot
			logTask(rs.task, TaskCancelled, 0)
			log.Printf("[PENDING] %s истёк без ответа", rs.pending.ID)
			if notify != nil {
				notify(rs.pending.ChatID, rs.pending.Channel,
					"⌛ Подтверждение не пришло за 5 минут — действие отменил, задачу закрыл.")
			}
		}
	}
}
