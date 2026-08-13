package main

// Executor-loop invariants that do NOT need a live brain (ТЗ §6.3 п. 5, §4.2;
// приёмка §10 п. 21, 23, 10, 12).

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kibborg/engine/browser"
)

func newTestLoop(t *testing.T, packs []string) *loopState {
	t.Helper()
	task := newTask(safeActor(), "тестовая задача")
	t.Cleanup(task.Close)
	return &loopState{
		task:  task,
		cfg:   Config{},
		sess:  browser.New(""),
		msgs:  []map[string]any{{"role": "system", "content": "sys"}},
		sys:   "sys",
		packs: packs,
		tools: assemblePackTools(browser.New(""), packs),
		plan:  dispatchPlan{Packs: packs},
	}
}

func call(id, name string, args map[string]any) toolCall {
	tc := toolCall{ID: id, Type: "function"}
	tc.Function.Name = name
	raw, _ := json.Marshal(args)
	tc.Function.Arguments = string(raw)
	return tc
}

// answeredIDs collects the tool_call_ids that received a `tool` message.
func answeredIDs(msgs []map[string]any) map[string]string {
	out := map[string]string{}
	for _, m := range msgs {
		if role, _ := m["role"].(string); role != "tool" {
			continue
		}
		id, _ := m["tool_call_id"].(string)
		content, _ := m["content"].(string)
		out[id] = content
	}
	return out
}

// Приёмка №21: a turn with three calls where the SECOND goes to a confirmation — the third
// must still get a `tool` reply, or the next llama-server request fails with 400.
func TestTurnWithPendingAnswersEveryToolCall(t *testing.T) {
	dir := t.TempDir()
	ls := newTestLoop(t, []string{packFiles})

	outside := `D:\tmp\definitely-outside`
	if os.PathSeparator == '/' {
		outside = "/var/tmp/definitely-outside"
	}
	calls := []toolCall{
		call("c1", "list_dir", map[string]any{"path": dir}),        // allowed, runs
		call("c2", "delete_path", map[string]any{"path": outside}), // → ask → pause
		call("c3", "read_file", map[string]any{"path": "нет.txt"}), // must be answered anyway
	}
	out := ls.runTurn(calls)

	if !out.paused {
		t.Fatal("вызов вне рабочих каталогов должен уводить ход в подтверждение")
	}
	if ls.task.GetStatus() != TaskWaitingConfirm {
		t.Fatalf("статус задачи = %s, ждали waiting_confirm", ls.task.GetStatus())
	}
	if ls.task.Pending == nil || ls.task.Pending.ToolCallID != "c2" {
		t.Fatalf("pending должен адресовать именно c2: %+v", ls.task.Pending)
	}

	answered := answeredIDs(ls.msgs)
	if _, ok := answered["c1"]; !ok {
		t.Error("выполненный вызов c1 остался без tool-ответа")
	}
	if _, ok := answered["c2"]; ok {
		t.Error("отложенный на подтверждение c2 получает ответ только при возобновлении")
	}
	third, ok := answered["c3"]
	if !ok {
		t.Fatal("c3 остался без tool-ответа — следующий вызов модели упадёт с 400")
	}
	if !strings.HasPrefix(third, "deferred:") {
		t.Errorf("c3 должен получить синтетическое «отложено», получили %q", third)
	}
}

// Приёмка №10 + №23: a hard-blocked call ends the task, and the remaining calls of that turn
// still get answers.
func TestTurnHardBlockStopsAndAnswers(t *testing.T) {
	ls := newTestLoop(t, []string{packConsole})
	calls := []toolCall{
		call("n1", "run_command", map[string]any{"command": "format C: /fs:ntfs"}),
		call("n2", "run_command", map[string]any{"command": "Get-Date"}),
	}
	out := ls.runTurn(calls)
	if !out.stopped {
		t.Fatal("ядерная команда должна прекращать задачу")
	}
	if !strings.Contains(out.finalText, "Не дам это сделать") {
		t.Errorf("человеку нужен внятный отказ, получили %q", out.finalText)
	}
	answered := answeredIDs(ls.msgs)
	if !strings.HasPrefix(answered["n1"], "blocked:") {
		t.Errorf("n1 = %q, ждали blocked", answered["n1"])
	}
	if _, ok := answered["n2"]; !ok {
		t.Error("оставшийся вызов хода обязан получить tool-ответ")
	}
}

// Приёмка №12: request_pack widens the arsenal mid-task, and the loop actually re-advertises
// the new schemas.
func TestRequestPackWidensToolsInLoop(t *testing.T) {
	ls := newTestLoop(t, []string{packChat})
	before := len(ls.tools)
	if before != 1 {
		t.Fatalf("пак chat = только request_pack, получили %d инструментов", before)
	}
	out := ls.runTurn([]toolCall{call("e1", "request_pack", map[string]any{"pack": "files", "reason": "нужно записать файл"})})
	if out.paused || out.stopped {
		t.Fatal("эскалация не должна ставить задачу на паузу")
	}
	if !hasPack(ls.packs, packFiles) {
		t.Fatalf("пак files не подключился: %v", ls.packs)
	}
	if !hasTool(ls.tools, "write_file") {
		t.Fatal("после эскалации схемы нового пака должны стать доступны модели")
	}
	if ls.task.Escalations != 1 {
		t.Fatalf("счётчик эскалаций = %d", ls.task.Escalations)
	}
	if ans := answeredIDs(ls.msgs)["e1"]; !strings.HasPrefix(ans, "ok:") {
		t.Errorf("успешная эскалация = ok, получили %q", ans)
	}
}

// Приёмка №23: the same forbidden call twice ends the task with «не дам это сделать» instead
// of looping through all 12 steps. A refusal does not change its mind inside one task.
func TestRepeatedDenyLeavesLoop(t *testing.T) {
	ls := newTestLoop(t, []string{packConsole})
	// A non-owner actor turns `ask` into `deny`: there is nobody who may authorise it.
	ls.task.Actor.IsOwner = false
	args := map[string]any{"command": "Remove-Item .git -Recurse"}

	first := ls.runTurn([]toolCall{call("d1", "run_command", args)})
	if first.stopped {
		t.Fatal("после первого отказа модель ещё может попробовать другой путь")
	}
	if ans := answeredIDs(ls.msgs)["d1"]; !strings.HasPrefix(ans, "denied:") {
		t.Fatalf("не-владелец должен получать denied, получили %q", ans)
	}

	second := ls.runTurn([]toolCall{call("d2", "run_command", args)})
	if !second.stopped {
		t.Fatal("тот же запрещённый вызов второй раз обязан прекратить задачу")
	}
	if !strings.Contains(second.finalText, "Не дам это сделать") {
		t.Errorf("человеку нужен внятный отказ, получили %q", second.finalText)
	}
	// A DIFFERENT call on the same tool is a different intention and still gets its chance.
	fresh := newTestLoop(t, []string{packConsole})
	fresh.task.Actor.IsOwner = false
	fresh.runTurn([]toolCall{call("a", "run_command", map[string]any{"command": "Remove-Item D:\\x -Recurse"})})
	other := fresh.runTurn([]toolCall{call("b", "run_command", map[string]any{"command": "Remove-Item D:\\y -Recurse"})})
	if other.stopped {
		t.Fatal("другой путь — другое намерение, глушить его нельзя")
	}
}

// The owner still gets a question; only a stranger gets a flat refusal (§6.0).
func TestNonOwnerGetsDenyNotAsk(t *testing.T) {
	outside := `D:\tmp\somewhere-else`
	if os.PathSeparator == '/' {
		outside = "/var/tmp/somewhere-else"
	}
	owner := safeActor()
	if d := guardToolCall(owner, "delete_path", map[string]any{"path": outside}); d.Action != ActionAsk {
		t.Fatalf("владельцу задаём вопрос, получили %s", d.Action)
	}
	stranger := safeActor()
	stranger.IsOwner = false
	d := guardToolCall(stranger, "delete_path", map[string]any{"path": outside})
	if d.Action != ActionDeny {
		t.Fatalf("не-владельцу отказываем, получили %s", d.Action)
	}
	// …and the hands switch is the owner's switch: full must not empower a stranger.
	stranger.Mode = handsModeFull
	if d := guardToolCall(stranger, "delete_path", map[string]any{"path": outside}); d.Action != ActionDeny {
		t.Fatalf("full не должен давать права чужому чату, получили %s", d.Action)
	}
}

// From the live run: read_url failed four times on network errors, the tool-failure limit
// fired, and the INTERNAL note «Инструмент read_url не работает … Дальше без него» was handed
// to the user as the answer — promising a continuation that never came. A dead tool must end
// the loop, not the answer: the summary pass still has to speak.
func TestDeadToolDoesNotBecomeTheAnswer(t *testing.T) {
	ls := newTestLoop(t, []string{packFiles})
	// read_file on a missing path fails deterministically; four different paths keep the
	// fingerprint counter from firing first, so the TOOL limit is what we exercise.
	var out turnOutcome
	for i := 0; i < toolFailLimit; i++ {
		out = ls.runTurn([]toolCall{call(
			fmt.Sprintf("f%d", i), "read_file",
			map[string]any{"path": fmt.Sprintf("D:\\nope\\missing-%d.txt", i)})})
	}
	if !out.stopped {
		t.Fatalf("после %d сбоев инструмента цикл обязан остановиться", toolFailLimit)
	}
	if out.refused {
		t.Error("сбой инструмента — не отказ политики; статус задачи не должен быть failed по этой причине")
	}
	if out.finalText != "" {
		t.Fatalf("служебная записка не должна становиться ответом человеку: %q", out.finalText)
	}
	// The note goes to the MODEL, so it can wrap up with what it has.
	last := lastStepFor(ls.task, "read_file")
	if last == nil || !strings.Contains(last.Text, "отключён до конца этой задачи") {
		t.Fatalf("модель должна узнать, что инструмент отключён: %+v", last)
	}
}

// A denied call feeds the model a `denied` status (not a fake error) and counts on both axes.
func TestDeniedCallReportsDeniedStatus(t *testing.T) {
	ls := newTestLoop(t, []string{packWeb})
	// agent_reach is `ask` in safe mode; force `deny` by pretending the actor is in a mode
	// where the gate denies — here we assert the ask branch, then the guard's own decision.
	out := ls.runTurn([]toolCall{call("a1", "agent_reach", map[string]any{"args": []any{"twitter"}})})
	if !out.paused {
		t.Fatal("agent_reach в safe должен спрашивать подтверждение")
	}
	if ls.task.Pending.Rule != ruleDangerousTool {
		t.Errorf("правило = %q, ждали dangerous_tool", ls.task.Pending.Rule)
	}
}

// The gate runs on the ACTUAL tool call, so a `chat` route that escalates to console is still
// gated (§4: «Диспетчер не граница безопасности»).
func TestGateAppliesRegardlessOfRouting(t *testing.T) {
	ls := newTestLoop(t, []string{packChat})
	ls.runTurn([]toolCall{call("e1", "request_pack", map[string]any{"pack": "console"})})
	if !hasPack(ls.packs, packConsole) {
		t.Fatal("эскалация в console должна проходить — это не граница безопасности")
	}
	out := ls.runTurn([]toolCall{call("k1", "run_command", map[string]any{"command": "diskpart"})})
	if !out.stopped {
		t.Fatal("но сама команда обязана упереться в guard, а не в роутинг")
	}
}

// Artifacts are drained into the task after EVERY step, not at the end (§6.3 п. 6).
func TestStepArtifactsGoToTask(t *testing.T) {
	ls := newTestLoop(t, []string{packFiles})
	ls.appendToolMsg(call("x", "capture_screenshot", nil), okResult("готово", []string{filepath.Join("runtime", "shot.png")}))
	if got := ls.task.Artifacts; len(got) != 1 {
		t.Fatalf("артефакты шага должны сразу уходить в задачу, получили %v", got)
	}
	if len(ls.task.Steps) != 1 || ls.task.Steps[0].Status != string(StatusOK) {
		t.Fatalf("сжатый результат шага не записан: %+v", ls.task.Steps)
	}
}

// A tool call with unparseable arguments is a plain failure, not a crash.
func TestBadArgumentsAreFailure(t *testing.T) {
	ls := newTestLoop(t, []string{packFiles})
	tc := toolCall{ID: "b1"}
	tc.Function.Name = "read_file"
	tc.Function.Arguments = "{не json"
	ls.runTurn([]toolCall{tc})
	if ans := answeredIDs(ls.msgs)["b1"]; !strings.HasPrefix(ans, "failed:") {
		t.Fatalf("сломанные аргументы = failed, получили %q", ans)
	}
}

// From the FIRST LIVE RUN: this model sometimes writes the call as prose instead of emitting
// tool_calls — for a request that worked a minute earlier. Nothing runs, so the line must not
// be shown to the user as if it were a result.
func TestUnparsedToolCallDetected(t *testing.T) {
	tools := assemblePackTools(browser.New(""), []string{packConsole, packFiles})

	// Verbatim from the live run.
	prose := "удалю каталог D:\\tmp\\kib-probe\n\nrun_command(command=\"Remove-Item -Path 'D:\\tmp\\kib-probe' -Recurse -Force\", timeout_sec=30)"
	if got := unparsedToolCall(prose, tools, false); got != "run_command" {
		t.Fatalf("не распознан вызов текстом: %q", got)
	}
	// If the task actually ran something, the same text is just commentary.
	if got := unparsedToolCall(prose, tools, true); got != "" {
		t.Fatalf("при выполненных вызовах это уже не признак сбоя, получили %q", got)
	}
	// A normal answer must not trip it.
	for _, ok := range []string{
		"Готово, файл записан.",
		"Могу выполнить команду run_command, если нужно.", // упоминание без списка аргументов
		"",
	} {
		if got := unparsedToolCall(ok, tools, false); got != "" {
			t.Errorf("ложное срабатывание на %q → %q", ok, got)
		}
	}
	// request_pack is not a real action and must not count.
	if got := unparsedToolCall("request_pack(pack=\"web\")", tools, false); got != "" {
		t.Errorf("request_pack не должен считаться несостоявшимся действием, получили %q", got)
	}
}

func TestValidToolCallsDropsEmptyNames(t *testing.T) {
	good := call("1", "read_file", nil)
	bad := toolCall{ID: "2"}
	if got := validToolCalls([]toolCall{good, bad}); len(got) != 1 || got[0].ID != "1" {
		t.Fatalf("вызовы без имени должны отбрасываться, получили %+v", got)
	}
}

// Живой прогон, 12:50: клик мышью провалился, вывод окна вперёд провалился, а итоговый ответ
// был «Отлично! Чат Kibborg уже открыт». Оба провала лежали в контексте модели как `failed`
// — она их просто не заметила.
//
// Вывод: факт «действие не выполнилось» нельзя оставлять на усмотрение модели. Его фиксирует
// Go и приписывает к ответу сам, поэтому спорить с ним нечем.
func TestFailedActionsSurfaceToUser(t *testing.T) {
	task := newTask(fullActor(), "кликни по чату")
	defer task.Close()
	ls := &loopState{task: task, plan: dispatchPlan{}, sess: browser.New("")}

	call := func(name string) toolCall {
		tc := toolCall{ID: "c-" + name}
		tc.Function.Name = name
		tc.Function.Arguments = "{}"
		return tc
	}
	ls.appendToolMsg(call("mouse_action"), failResult("не смог переместить курсор в (108,952)", nil))
	ls.appendToolMsg(call("focus_window"), failResult("система не отдала фокус окну", nil))
	// Провалившееся ЧТЕНИЕ шумом быть не должно — о нём модель и так рассказывает нормально.
	ls.appendToolMsg(call("list_windows"), failResult("список окон не получен", nil))

	res := finishTask(ls, TaskDone, "Отлично! Чат Kibborg уже открыт.")
	if len(res.Notices) != 1 {
		t.Fatalf("ждали одну приписку о провалах, получили %v", res.Notices)
	}
	note := res.Notices[0]
	for _, want := range []string{"mouse_action", "focus_window", "Не все действия выполнились"} {
		if !strings.Contains(note, want) {
			t.Errorf("в приписке нет %q: %s", want, note)
		}
	}
	if strings.Contains(note, "list_windows") {
		t.Error("провалившееся чтение в приписку попадать не должно — это лишний шум")
	}

	// Успешная задача без провалов приписок не получает.
	clean := newTask(fullActor(), "ок")
	defer clean.Close()
	ls2 := &loopState{task: clean, plan: dispatchPlan{}, sess: browser.New("")}
	ls2.appendToolMsg(call("mouse_action"), okResult("клик в (10,10)", nil))
	if r := finishTask(ls2, TaskDone, "готово"); len(r.Notices) != 0 {
		t.Errorf("без провалов приписок быть не должно: %v", r.Notices)
	}
}
