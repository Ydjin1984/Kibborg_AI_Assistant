package main

// Resuming after a confirmation (ТЗ §6.3 п. 8, приёмка §10 п. 7).
// The property under test is the expensive one: «да» must execute ONLY the confirmed tool and
// must NOT replay the steps already done. run_command is not idempotent, a downloaded video is
// downloaded, a written file is written.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pauseOnDelete drives one turn that writes a file and then hits a confirmation, returning the
// parked state plus the path of the file that must NOT be written twice.
func pauseOnDelete(t *testing.T) (*resumeState, string) {
	t.Helper()
	root := t.TempDir()
	oldRoots := handsRootsExtra
	setHandsRoots(root)
	t.Cleanup(func() { handsRootsExtra = oldRoots })

	marker := filepath.Join(root, "marker.txt")
	outside := `D:\tmp\definitely-outside-kibborg`
	if os.PathSeparator == '/' {
		outside = "/var/tmp/definitely-outside-kibborg"
	}

	ls := newTestLoop(t, []string{packFiles})
	ls.task.Actor.ChatID = 91001
	ls.task.ChatID = 91001

	out := ls.runTurn([]toolCall{
		call("w1", "write_file", map[string]any{"path": marker, "content": "шаг1\n", "append": true}),
		call("d1", "delete_path", map[string]any{"path": outside}),
	})
	if !out.paused {
		t.Fatal("удаление вне рабочих каталогов должно уйти в подтверждение")
	}
	rs := &resumeState{
		task:    ls.task,
		cfg:     ls.cfg,
		pending: ls.task.Pending,
		msgs:    compactToolMessages(ls.msgs),
		sys:     ls.sys,
		packs:   ls.packs,
		tools:   ls.tools,
	}
	savePending(rs)
	return rs, marker
}

// «Нет» closes the task, writes a refusal into the transcript (so the model does not retry),
// and repeats nothing.
func TestResumeRefusalDoesNotReplay(t *testing.T) {
	rs, marker := pauseOnDelete(t)
	before, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("первый шаг должен был записать файл: %v", err)
	}

	res := resumeConfirmed(takePending(rs.task.ChatID), false, nil)

	if res.Status != TaskCancelled {
		t.Fatalf("после отказа статус = %s, ждали cancelled", res.Status)
	}
	if !strings.Contains(res.Text, "Отменил") {
		t.Errorf("человеку нужен внятный ответ, получили %q", res.Text)
	}
	after, _ := os.ReadFile(marker)
	if string(after) != string(before) {
		t.Fatalf("уже сделанный шаг переигран: было %q, стало %q", before, after)
	}
	// The transcript must be complete: the pending call finally has its tool reply, so the
	// next request to llama-server does not hit a missing tool_call_id.
	if st := lastStepFor(rs.task, "delete_path"); st == nil || st.Status != string(StatusDenied) {
		t.Fatalf("отложенный вызов должен получить denied-ответ, шаги: %+v", rs.task.Steps)
	}
	if peekPending(rs.task.ChatID) != nil {
		t.Error("после ответа ожидание должно быть снято")
	}
}

// «Да» executes the confirmed tool once and still does not replay the earlier steps. The loop
// afterwards tries to reach the brain (absent in tests) and ends as failed — which is fine:
// what matters here is the side effects, not the final wording.
func TestResumeApprovalRunsOnlyConfirmedTool(t *testing.T) {
	rs, marker := pauseOnDelete(t)
	before, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}

	res := resumeConfirmed(takePending(rs.task.ChatID), true, nil)

	after, _ := os.ReadFile(marker)
	if string(after) != string(before) {
		t.Fatalf("шаг до подтверждения переигран: было %q, стало %q", before, after)
	}
	if res.TaskID != rs.task.ID {
		t.Errorf("возобновление должно продолжать ту же задачу: %s vs %s", res.TaskID, rs.task.ID)
	}
	st := lastStepFor(rs.task, "delete_path")
	if st == nil {
		t.Fatal("подтверждённый вызов обязан получить tool-ответ")
	}
	// The path does not exist, so the delete fails — but it was ATTEMPTED, which is the point.
	if st.Status == string(StatusDenied) || st.Status == string(StatusDeferred) {
		t.Fatalf("после «да» вызов должен исполниться, статус = %s", st.Status)
	}
	if peekPending(rs.task.ChatID) != nil {
		t.Error("после ответа ожидание должно быть снято")
	}
}

func TestTakePendingMatchingBindsID(t *testing.T) {
	rs, _ := pauseOnDelete(t)
	id := rs.pending.ID
	if takePendingMatching(rs.task.ChatID, "wrong-id-"+id) != nil {
		t.Fatal("чужой id не должен снимать pending")
	}
	if peekPending(rs.task.ChatID) == nil {
		t.Fatal("pending должен остаться после mismatch")
	}
	got := takePendingMatching(rs.task.ChatID, id)
	if got == nil || got.pending.ID != id {
		t.Fatal("верный id должен снять именно этот pending")
	}
	if peekPending(rs.task.ChatID) != nil {
		t.Fatal("после match pending пуст")
	}
}

// The compacted steps are small enough to sit in RAM across a pause (§6.3 п. 4).
func TestPendingKeepsCompactStepsNotRawTranscript(t *testing.T) {
	rs, _ := pauseOnDelete(t)
	defer takePending(rs.task.ChatID)

	for _, s := range rs.task.Steps {
		if len(s.Text) > 500 {
			t.Errorf("сжатый шаг слишком большой (%d символов) — это уже не сводка", len(s.Text))
		}
	}
	// Only metadata reaches the disk — never the message list.
	p := stalePendingOnDisk()
	if p == nil {
		t.Fatal("метаданные ожидания должны лежать на диске")
	}
	if p.Tool != "delete_path" || p.TaskID != rs.task.ID {
		t.Fatalf("метаданные не про тот вызов: %+v", p)
	}
	raw, err := os.ReadFile(pendingStatePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "tool_call_id\":\"w1") || strings.Contains(string(raw), "шаг1") {
		t.Error("на диск ушли сообщения диалога — должны быть только метаданные")
	}
}

// lastStepFor finds the most recent compacted step for a tool — the production-visible record
// that a call actually got an answer.
func lastStepFor(task *Task, tool string) *CompactResult {
	for i := len(task.Steps) - 1; i >= 0; i-- {
		if task.Steps[i].Tool == tool {
			return &task.Steps[i]
		}
	}
	return nil
}

func TestPendingQuestionMentionsToolAndReason(t *testing.T) {
	p := &Pending{Tool: "delete_path", Args: map[string]any{"path": `D:\tmp\old`}, Reason: "удаление вне рабочих каталогов"}
	q := p.question()
	for _, want := range []string{"delete_path", `D:\tmp\old`, "удаление вне рабочих каталогов", "да", "нет", "5 минут"} {
		if !strings.Contains(q, want) {
			t.Errorf("в вопросе нет %q:\n%s", want, q)
		}
	}
}
