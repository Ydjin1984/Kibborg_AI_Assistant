package main

// End-to-end pipeline against a FAKE llama-server (ТЗ §4 целиком).
//
// Every other test in this package stops at the edge of the model. This one drives the whole
// path — dispatcher → packs → guarded tools → answer — with a scripted brain, and, crucially,
// VALIDATES WHAT LEAVES THE PROCESS: the fake server checks the OpenAI protocol invariant that
// llama.cpp's jinja template enforces with a 400 — every assistant tool_call must have a
// matching `tool` message before the next request (§6.3 п. 5).
//
// It does not test whether Qwen returns good JSON — that needs the real brain on real hardware.
// It tests that our side of the contract is correct no matter what the model says.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeBrain is a scripted llama-server: it answers requests in order from a queue and records
// every request body it saw.
type fakeBrain struct {
	t        *testing.T
	srv      *httptest.Server
	port     int
	mu       sync.Mutex
	replies  []string       // JSON bodies of assistant messages, in order
	requests []fakeBrainReq // what we were asked, in order
	protoErr []string       // protocol violations detected
	// onRequest fires after request n has been recorded, before the reply is written. It is
	// how a test injects an event (like /stop) at an exact point in the loop.
	onRequest func(n int)
}

type fakeBrainReq struct {
	Messages []map[string]any `json:"messages"`
	Tools    []struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	} `json:"tools"`
}

// newFakeBrain starts the server on loopback and returns it with the port llmChatTools needs.
// httptest listens on 127.0.0.1, which is exactly where llmChatTools points.
func newFakeBrain(t *testing.T, replies ...string) *fakeBrain {
	t.Helper()
	fb := &fakeBrain{t: t, replies: replies}
	fb.srv = httptest.NewServer(http.HandlerFunc(fb.handle))
	t.Cleanup(fb.srv.Close)

	// http://127.0.0.1:PORT → PORT
	addr := strings.TrimPrefix(fb.srv.URL, "http://")
	_, portStr, err := splitHostPort(addr)
	if err != nil {
		t.Fatalf("не разобрал адрес фейкового сервера %q: %v", fb.srv.URL, err)
	}
	fb.port, err = strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}
	return fb
}

func splitHostPort(addr string) (string, string, error) {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return "", "", fmt.Errorf("нет порта в %q", addr)
	}
	return addr[:i], addr[i+1:], nil
}

func (fb *fakeBrain) handle(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/health") {
		w.WriteHeader(http.StatusOK)
		return
	}
	body, _ := io.ReadAll(r.Body)
	var req fakeBrainReq
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}

	fb.mu.Lock()
	fb.requests = append(fb.requests, req)
	n := len(fb.requests)
	if errs := checkToolProtocol(req.Messages); len(errs) > 0 {
		fb.protoErr = append(fb.protoErr, errs...)
	}
	var reply string
	if len(fb.replies) > 0 {
		reply, fb.replies = fb.replies[0], fb.replies[1:]
	} else {
		reply = `{"content":"больше сказать нечего"}`
	}
	hook := fb.onRequest
	fb.mu.Unlock()
	if hook != nil {
		hook(n)
	}

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"choices":[{"message":%s}],"timings":{"prompt_n":1234,"prompt_ms":567.8,"predicted_n":42}}`, reply)
}

// checkToolProtocol is the rule llama-server enforces with a 400: every tool_call id in an
// assistant message must be answered by a `tool` message before the request is sent.
func checkToolProtocol(msgs []map[string]any) []string {
	var errs []string
	pending := map[string]string{}
	for _, m := range msgs {
		role, _ := m["role"].(string)
		switch role {
		case "assistant":
			raw, ok := m["tool_calls"]
			if !ok {
				continue
			}
			calls, _ := raw.([]any)
			for _, c := range calls {
				cm, _ := c.(map[string]any)
				id, _ := cm["id"].(string)
				fn, _ := cm["function"].(map[string]any)
				name, _ := fn["name"].(string)
				if id == "" {
					errs = append(errs, "tool_call без id")
					continue
				}
				pending[id] = name
			}
		case "tool":
			id, _ := m["tool_call_id"].(string)
			if _, ok := pending[id]; !ok {
				errs = append(errs, "tool-ответ на неизвестный tool_call_id "+id)
			}
			delete(pending, id)
		}
	}
	for id, name := range pending {
		errs = append(errs, "tool_call "+name+" (id "+id+") остался без tool-ответа")
	}
	return errs
}

func (fb *fakeBrain) assertProtocolClean() {
	fb.t.Helper()
	fb.mu.Lock()
	defer fb.mu.Unlock()
	for _, e := range fb.protoErr {
		fb.t.Errorf("протокол tool-calls нарушен: %s (llama-server ответил бы 400)", e)
	}
}

// toolsOfRequest returns the tool names advertised in the Nth request (0-based).
func (fb *fakeBrain) toolsOfRequest(n int) []string {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	if n >= len(fb.requests) {
		return nil
	}
	var out []string
	for _, tl := range fb.requests[n].Tools {
		out = append(out, tl.Function.Name)
	}
	return out
}

func (fb *fakeBrain) requestCount() int {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	return len(fb.requests)
}

// assistantToolCall builds a scripted assistant reply containing one tool call.
func assistantToolCall(id, name string, args map[string]any) string {
	raw, _ := json.Marshal(args)
	return fmt.Sprintf(
		`{"content":"","tool_calls":[{"id":%q,"type":"function","function":{"name":%q,"arguments":%q}}]}`,
		id, name, string(raw))
}

func assistantText(text string) string {
	raw, _ := json.Marshal(text)
	return fmt.Sprintf(`{"content":%s}`, raw)
}

// ===== the tests =====

// Full happy path: dispatcher routes to `files`, the executor writes a file, then answers.
// Checks the four things §4 promises end-to-end.
func TestE2EDispatcherToolAndAnswer(t *testing.T) {
	work := t.TempDir()
	oldRoots := handsRootsExtra
	setHandsRoots(work)
	t.Cleanup(func() { handsRootsExtra = oldRoots })

	target := filepath.Join(work, "note.md")
	fb := newFakeBrain(t,
		// 1) dispatcher
		assistantText(`{"packs":["files"],"plan":["записать заметку"],"confirm":false,"summary":"сохраню заметку"}`),
		// 2) executor: one tool call
		assistantToolCall("t1", "write_file", map[string]any{"path": target, "content": "привет"}),
		// 3) executor: final prose
		assistantText("Записал заметку в файл."),
	)

	res := runLayeredAgent(agentRequest{
		cfg:      Config{BrainPort: fb.port},
		actor:    Actor{Mode: handsModeSafe, Channel: channelWeb, ChatID: 55001, IsOwner: true},
		baseMsgs: []map[string]any{{"role": "system", "content": "sys"}},
		input:    "сохрани заметку",
	})

	fb.assertProtocolClean()
	if res.Status != TaskDone {
		t.Fatalf("статус = %s, ждали done (текст: %s)", res.Status, res.Text)
	}
	// summary is the first line (§4.1)
	if !strings.HasPrefix(res.Text, "сохраню заметку") {
		t.Errorf("summary должен быть первой строкой, получили: %q", capAgentText(res.Text, 100))
	}
	if !strings.Contains(res.Text, "Записал заметку") {
		t.Errorf("итог модели потерялся: %q", res.Text)
	}
	if len(res.Plan) != 1 || res.Plan[0] != "записать заметку" {
		t.Errorf("plan не доехал до канала: %v", res.Plan)
	}
	// The tool actually ran.
	if data, err := os.ReadFile(target); err != nil || string(data) != "привет" {
		t.Fatalf("write_file не выполнился: %v / %q", err, data)
	}
	// Layer 2 advertised exactly the `files` pack plus the always-on escalation, and the
	// dispatcher call itself carried NO tools (§4: «Диспетчер (тулов нет)»).
	if tools := fb.toolsOfRequest(0); len(tools) != 0 {
		t.Errorf("диспетчер не должен видеть инструменты, получил %v", tools)
	}
	execTools := fb.toolsOfRequest(1)
	if !containsAll(execTools, []string{"write_file", "read_file", "delete_path", "request_pack"}) {
		t.Errorf("исполнителю выдали не тот пак: %v", execTools)
	}
	for _, unwanted := range []string{"run_command", "web_search", "click_element"} {
		if containsAll(execTools, []string{unwanted}) {
			t.Errorf("в паке files не должно быть %s: %v", unwanted, execTools)
		}
	}
}

// §3.2: prompt_ms of the FIRST executor turn is what lands in tasks.jsonl.
func TestE2EFirstPromptMsRecorded(t *testing.T) {
	dir := t.TempDir()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)

	fb := newFakeBrain(t,
		assistantText(`{"packs":["chat"],"plan":[],"confirm":false,"summary":"отвечу"}`),
		assistantText("Привет!"),
	)
	res := runLayeredAgent(agentRequest{
		cfg:      Config{BrainPort: fb.port},
		actor:    Actor{Mode: handsModeSafe, Channel: channelWeb, ChatID: 55002, IsOwner: true},
		baseMsgs: []map[string]any{{"role": "system", "content": "sys"}},
		input:    "привет",
	})
	if res.Status != TaskDone {
		t.Fatalf("статус = %s", res.Status)
	}
	line, err := os.ReadFile(tasksLogPath)
	if err != nil {
		t.Fatal(err)
	}
	var rec taskRecord
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(line))), &rec); err != nil {
		t.Fatal(err)
	}
	if rec.FirstPromptMs != 567 || rec.FirstPromptTokens != 1234 {
		t.Fatalf("prompt_ms первого хода исполнителя не записан: %+v", rec)
	}
	if rec.TotalMs <= 0 {
		t.Error("total_ms должен быть заполнен")
	}
}

// A turn with three calls where the middle one needs a confirmation: the task parks, and when
// the answer comes the loop continues from the compacted steps — with the protocol intact on
// BOTH requests (this is the 400 that §6.3 п. 5 exists to prevent).
func TestE2EPendingThenResumeKeepsProtocol(t *testing.T) {
	work := t.TempDir()
	oldRoots := handsRootsExtra
	setHandsRoots(work)
	t.Cleanup(func() { handsRootsExtra = oldRoots })

	outside := `D:\tmp\e2e-outside`
	if os.PathSeparator == '/' {
		outside = "/var/tmp/e2e-outside"
	}
	marker := filepath.Join(work, "step1.txt")

	// The assistant asks for three tools at once; the second one is the dangerous one.
	threeCalls := fmt.Sprintf(`{"content":"","tool_calls":[
		{"id":"a1","type":"function","function":{"name":"write_file","arguments":%q}},
		{"id":"a2","type":"function","function":{"name":"delete_path","arguments":%q}},
		{"id":"a3","type":"function","function":{"name":"list_dir","arguments":%q}}]}`,
		`{"path":`+strconv.Quote(marker)+`,"content":"шаг1"}`,
		`{"path":`+strconv.Quote(outside)+`}`,
		`{"path":`+strconv.Quote(work)+`}`)

	fb := newFakeBrain(t,
		assistantText(`{"packs":["files"],"plan":["почистить"],"confirm":true,"summary":"почищу"}`),
		threeCalls,
		assistantText("Готово."),
	)
	actor := Actor{Mode: handsModeSafe, Channel: channelWeb, ChatID: 55003, IsOwner: true}
	res := runLayeredAgent(agentRequest{
		cfg:      Config{BrainPort: fb.port},
		actor:    actor,
		baseMsgs: []map[string]any{{"role": "system", "content": "sys"}},
		input:    "удали лишнее",
	})
	if !res.Waiting || res.Status != TaskWaitingConfirm {
		t.Fatalf("ждали паузу на подтверждении, получили %s / %q", res.Status, res.Text)
	}
	if !strings.Contains(res.Text, "delete_path") {
		t.Errorf("в вопросе не видно, что подтверждаем: %q", res.Text)
	}
	// The first step really ran before the pause.
	if _, err := os.ReadFile(marker); err != nil {
		t.Fatalf("шаг до подтверждения не выполнился: %v", err)
	}
	before := fb.requestCount()

	rs := takePending(actor.ChatID)
	if rs == nil {
		t.Fatal("задача не припаркована")
	}
	out := resumeConfirmed(rs, true, nil)

	fb.assertProtocolClean()
	if fb.requestCount() <= before {
		t.Fatal("после подтверждения цикл должен продолжиться новым запросом к модели")
	}
	if out.Status != TaskDone {
		t.Fatalf("после «да» задача должна завершиться, получили %s (%q)", out.Status, out.Text)
	}
	// The already-written file was not rewritten by a replay.
	data, _ := os.ReadFile(marker)
	if string(data) != "шаг1" {
		t.Fatalf("шаг переигран: %q", data)
	}
}

// request_pack mid-task must widen the schemas the NEXT request advertises (§4.3).
//
// Escalates to `files` rather than `web` so the follow-up tool call runs offline and
// deterministically — and so the task ends with real work done, not with an unverified answer.
func TestE2EEscalationWidensNextRequest(t *testing.T) {
	dir := t.TempDir()
	fb := newFakeBrain(t,
		assistantText(`{"packs":["chat"],"plan":[],"confirm":false,"summary":"посмотрю"}`),
		assistantToolCall("e1", "request_pack", map[string]any{"pack": "files", "reason": "нужно посмотреть каталог"}),
		assistantToolCall("e2", "list_dir", map[string]any{"path": dir}),
		assistantText("Готово, каталог пуст."),
	)
	res := runLayeredAgent(agentRequest{
		cfg:      Config{BrainPort: fb.port},
		actor:    Actor{Mode: handsModeSafe, Channel: channelWeb, ChatID: 55004, IsOwner: true},
		baseMsgs: []map[string]any{{"role": "system", "content": "sys"}},
		input:    "посмотри что там",
	})
	fb.assertProtocolClean()
	if res.Status != TaskDone {
		t.Fatalf("статус = %s (%q)", res.Status, res.Text)
	}
	// Request 1 = chat pack (request_pack only); request 2 = after the escalation.
	if tools := fb.toolsOfRequest(1); len(tools) != 1 || tools[0] != "request_pack" {
		t.Fatalf("пак chat должен нести только request_pack, получили %v", tools)
	}
	if tools := fb.toolsOfRequest(2); !containsAll(tools, []string{"read_file", "write_file", "list_dir", "request_pack"}) {
		t.Fatalf("после эскалации схемы files не подмешались: %v", tools)
	}
}

// A hard-blocked call ends the task without a further model round-trip, and the human gets a
// plain refusal (приёмка №10, §6.2).
func TestE2EHardBlockEndsTask(t *testing.T) {
	fb := newFakeBrain(t,
		assistantText(`{"packs":["console"],"plan":[],"confirm":false,"summary":"выполню"}`),
		assistantToolCall("n1", "run_command", map[string]any{"command": "diskpart /s wipe.txt"}),
	)
	res := runLayeredAgent(agentRequest{
		cfg:      Config{BrainPort: fb.port},
		actor:    Actor{Mode: handsModeSafe, Channel: channelWeb, ChatID: 55005, IsOwner: true},
		baseMsgs: []map[string]any{{"role": "system", "content": "sys"}},
		input:    "почисти диск",
	})
	fb.assertProtocolClean()
	if !strings.Contains(res.Text, "Не дам это сделать") {
		t.Fatalf("ждали внятный отказ, получили %q", res.Text)
	}
	if fb.requestCount() != 2 {
		t.Errorf("после hard_block лишних запросов к модели быть не должно, было %d", fb.requestCount())
	}
	// From the live run: a refusal must not be reported as a successful task, and the
	// dispatcher's «выполню» must not be pasted in front of «не дам это сделать».
	if res.Status != TaskFailed {
		t.Errorf("статус заблокированной задачи = %s, ждали failed", res.Status)
	}
	if strings.Contains(res.Text, "выполню") {
		t.Errorf("summary диспетчера противоречит отказу: %q", res.Text)
	}
}

// Сквозной сценарий, который на живом прогоне провалился целиком: «сделай скриншот рабочего
// стола и пришли его сюда».
//
// Тогда диспетчер отправил задачу в браузерный пак, агент открыл вкладку Chrome, capture_screenshot
// не нашёл к чему прицепиться, и пользователь получил лекцию про Win+Shift+S вместо картинки.
// Здесь проверяется вся цепочка: пак system → настоящий снимок экрана → файл В АРТЕФАКТАХ,
// потому что именно из артефактов оба канала отправляют картинку человеку.
func TestE2EDesktopScreenshotReachesUser(t *testing.T) {
	if !desktopSupported() {
		t.Skip("рабочий стол доступен только на Windows")
	}
	fb := newFakeBrain(t,
		assistantText(`{"packs":["system"],"plan":["снять экран"],"confirm":false,"summary":"снимаю экран"}`),
		assistantToolCall("s1", "capture_screen", map[string]any{}),
		assistantText("Держи скриншот рабочего стола."),
	)
	res := runLayeredAgent(agentRequest{
		cfg:      Config{BrainPort: fb.port},
		actor:    Actor{Mode: handsModeFull, Channel: channelWeb, ChatID: 55201, IsOwner: true},
		baseMsgs: []map[string]any{{"role": "system", "content": "sys"}},
		input:    "сделай скриншот рабочего стола и пришли сюда",
	})
	fb.assertProtocolClean()
	if res.Status != TaskDone {
		t.Fatalf("статус %s, ждали done: %s", res.Status, capAgentText(res.Text, 200))
	}
	if len(res.Artifacts) != 1 {
		t.Fatalf("скриншот обязан приехать артефактом — иначе пользователю нечего отправлять, получили %v", res.Artifacts)
	}
	t.Cleanup(func() { os.Remove(res.Artifacts[0]) })
	if st, err := os.Stat(res.Artifacts[0]); err != nil || st.Size() < 5000 {
		t.Fatalf("файл скриншота пуст или отсутствует: %v", err)
	}
	// И ни слова о том, что пользователь должен снять экран сам.
	if claimsNoTools(res.Text) {
		t.Errorf("ответ отрицает возможности, которые только что сработали: %q", res.Text)
	}
}

// Тот же diskpart, но руки развязаны. Это и есть новая граница «никаких ограничений, но с
// длиной рук»: недостижимых действий больше нет — ядерное не запрещается, а переспрашивается.
//
// Проверяем именно ПАУЗУ, а не allow: молчаливое форматирование диска по фразе «почисти диск»
// — это не свобода пользователя, а ошибка модели, которую пользователь не успел заметить.
func TestE2ENuclearInFullHandsAsksInsteadOfBlocking(t *testing.T) {
	chatID := int64(55105)
	t.Cleanup(func() { clearPending(chatID) })
	fb := newFakeBrain(t,
		assistantText(`{"packs":["console"],"plan":[],"confirm":true,"summary":"выполню"}`),
		assistantToolCall("n1", "run_command", map[string]any{"command": "diskpart /s wipe.txt"}),
	)
	res := runLayeredAgent(agentRequest{
		cfg:      Config{BrainPort: fb.port},
		actor:    Actor{Mode: handsModeFull, Channel: channelWeb, ChatID: chatID, IsOwner: true},
		baseMsgs: []map[string]any{{"role": "system", "content": "sys"}},
		input:    "почисти диск",
	})
	fb.assertProtocolClean()
	if !res.Waiting || res.Status != TaskWaitingConfirm {
		t.Fatalf("в длинных руках ядерная команда должна встать на подтверждение, получили %s/%v: %q",
			res.Status, res.Waiting, capAgentText(res.Text, 120))
	}
	if !strings.Contains(res.Text, "diskpart") {
		t.Errorf("вопрос обязан называть команду целиком: %q", res.Text)
	}
	if peekPending(chatID) == nil {
		t.Error("подтверждение должно быть сохранено и адресуемо словом «да»")
	}
}

// From the LIVE RUN: with the `web` pack active the model kept answering from memory — it even
// recited a stale error note out of the chat history as if it were fresh research — and the
// nudges eventually ran out. A factual answer produced without a single tool call is not an
// answer, and the user gets the failure instead of a plausible paragraph.
func TestE2EWebAnswerWithoutToolsIsRefused(t *testing.T) {
	invented := "Цена BRU — $0.42, ликвидность высокая, создатель — команда Bedrock."
	fb := newFakeBrain(t,
		assistantText(`{"packs":["web"],"plan":["найти данные"],"confirm":false,"summary":"найду"}`),
		// The model stonewalls: prose every time, never a tool call.
		assistantText(invented), assistantText(invented), assistantText(invented),
		assistantText(invented), assistantText(invented), assistantText(invented),
	)
	res := runLayeredAgent(agentRequest{
		cfg:      Config{BrainPort: fb.port},
		actor:    Actor{Mode: handsModeSafe, Channel: channelWeb, ChatID: 55008, IsOwner: true},
		baseMsgs: []map[string]any{{"role": "system", "content": "sys"}},
		input:    "что за монета BRU, какая цена?",
	})
	fb.assertProtocolClean()
	if res.Status != TaskFailed {
		t.Fatalf("ответ без единого инструмента = %s, ждали failed", res.Status)
	}
	if strings.Contains(res.Text, "0.42") {
		t.Fatalf("непроверенные цифры не должны доходить до пользователя: %q", res.Text)
	}
	if !strings.Contains(res.Text, "Не смог получить данные") {
		t.Errorf("нужен честный отказ, получили %q", res.Text)
	}
	// The nudges must have been tried before giving up — silence is not the answer either.
	if fb.requestCount() < 3 {
		t.Errorf("модель должна была получить несколько нуджей, запросов: %d", fb.requestCount())
	}
}

// A broken dispatcher answer must not break the turn: the fallback still routes it to `web`
// (§4.1), the executor gets the web schemas, and the agent returns a normal result — here a
// refusal, because this scripted model answers «Вот что нашёл» without searching anything,
// which is precisely the unverified answer that must not reach the user.
func TestE2EDispatcherGarbageFallsBackToWeb(t *testing.T) {
	fb := newFakeBrain(t,
		assistantText("конечно, сейчас посмотрю"),  // not JSON
		assistantText("всё ещё не JSON, извините"), // second attempt, also broken
		assistantText("Вот что нашёл."),            // executor answers without any tool
	)
	res := runLayeredAgent(agentRequest{
		cfg:      Config{BrainPort: fb.port},
		actor:    Actor{Mode: handsModeSafe, Channel: channelWeb, ChatID: 55006, IsOwner: true},
		baseMsgs: []map[string]any{{"role": "system", "content": "sys"}},
		input:    "что сегодня по BTC",
	})
	fb.assertProtocolClean()
	// Fallback bias is `web`, not `chat` — and the executor got the web schemas.
	if tools := fb.toolsOfRequest(2); !containsAll(tools, []string{"web_search", "read_url"}) {
		t.Fatalf("fallback должен смещаться в web, выданы: %v", tools)
	}
	if res.Status != TaskFailed {
		t.Fatalf("ответ без поиска = %s, ждали failed", res.Status)
	}
	if strings.Contains(res.Text, "Вот что нашёл") {
		t.Errorf("непроверенный ответ не должен доходить до пользователя: %q", res.Text)
	}
	if res.TaskID == "" {
		t.Error("задача всё равно должна быть зарегистрирована и залогирована")
	}
}

// /stop mid-task: the loop notices the cancelled context and answers «остановлено» instead of
// continuing to the next step (приёмка №16 на уровне цикла).
func TestE2EStopEndsLoop(t *testing.T) {
	work := t.TempDir()
	oldRoots := handsRootsExtra
	setHandsRoots(work)
	t.Cleanup(func() { handsRootsExtra = oldRoots })

	chatID := int64(55007)
	fb := newFakeBrain(t,
		assistantText(`{"packs":["files"],"plan":[],"confirm":false,"summary":"посмотрю"}`),
		assistantToolCall("s1", "list_dir", map[string]any{"path": work}),
		assistantText("этот ответ не должен понадобиться"),
	)
	// /stop arrives exactly while the executor's first turn is in flight — the moment the
	// spec cares about, reproduced deterministically instead of by racing.
	fb.onRequest = func(n int) {
		if n == 2 {
			stopActiveTask(chatID)
		}
	}
	res := runLayeredAgent(agentRequest{
		cfg:      Config{BrainPort: fb.port},
		actor:    Actor{Mode: handsModeSafe, Channel: channelWeb, ChatID: chatID, IsOwner: true},
		baseMsgs: []map[string]any{{"role": "system", "content": "sys"}},
		input:    "покажи файлы",
	})
	fb.assertProtocolClean()
	if res.Status != TaskCancelled {
		t.Fatalf("после /stop статус = %s (%q), ждали cancelled", res.Status, res.Text)
	}
	if !strings.Contains(res.Text, "Остановлено") {
		t.Fatalf("отменённая задача должна честно сказать об этом: %q", res.Text)
	}
	// The loop must not ask the model anything after being cancelled.
	if n := fb.requestCount(); n != 2 {
		t.Errorf("после отмены лишних запросов к модели быть не должно, было %d", n)
	}
}

// The protocol validator is only worth having if it actually fires — a checker that always
// passes is indistinguishable from no checker at all.
func TestToolProtocolCheckerDetectsGaps(t *testing.T) {
	assistant := func(ids ...string) map[string]any {
		var calls []any
		for _, id := range ids {
			calls = append(calls, map[string]any{
				"id":       id,
				"function": map[string]any{"name": "x"},
			})
		}
		return map[string]any{"role": "assistant", "tool_calls": calls}
	}
	toolMsg := func(id string) map[string]any {
		return map[string]any{"role": "tool", "tool_call_id": id, "content": "ok"}
	}

	if errs := checkToolProtocol([]map[string]any{assistant("a", "b"), toolMsg("a"), toolMsg("b")}); len(errs) != 0 {
		t.Fatalf("полный диалог не должен давать ошибок: %v", errs)
	}
	// This is exactly the 400 §6.3 п. 5 is about: a call left without an answer.
	errs := checkToolProtocol([]map[string]any{assistant("a", "b"), toolMsg("a")})
	if len(errs) == 0 || !strings.Contains(errs[0], "без tool-ответа") {
		t.Fatalf("пропущенный tool-ответ должен обнаруживаться, получили %v", errs)
	}
	if errs := checkToolProtocol([]map[string]any{toolMsg("zzz")}); len(errs) == 0 {
		t.Fatal("ответ на несуществующий tool_call_id должен обнаруживаться")
	}
}

func containsAll(haystack, needles []string) bool {
	set := make(map[string]bool, len(haystack))
	for _, h := range haystack {
		set[h] = true
	}
	for _, n := range needles {
		if !set[n] {
			return false
		}
	}
	return true
}
