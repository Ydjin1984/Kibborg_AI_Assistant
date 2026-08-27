package main

// Layers 2–4 of the agent (ТЗ §4): assemble the hands the dispatcher asked for, execute under
// the gate, answer the human. One core for Telegram and Web (AGENTS.md) — the channel only
// supplies an Actor and a status callback.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"kibborg/engine/browser"
)

// agentRequest is one user turn handed to the agent.
type agentRequest struct {
	cfg      Config
	actor    Actor
	baseMsgs []map[string]any // history + memory blocks (system first)
	input    string
	// status reports progress to the human («ищу», «читаю», «жду подтверждения»). CoT is
	// never streamed (§6) — only step statuses (plus structured phase/tool for Web).
	status StatusFn
	// memSummary is the long-term memory digest handed to the dispatcher (§4.1).
	memSummary string
	// hintPacks is what a slash command suggests (§5: «слэш → подсказка пака, не обход
	// диспетчера»). The dispatcher sees it as a hint and may still decide otherwise; it also
	// becomes the fallback if the dispatcher's JSON is broken twice.
	hintPacks []string
}

// agentResult is what the channel renders.
type agentResult struct {
	Text      string
	Artifacts []string
	Status    TaskStatus
	TaskID    string
	Plan      []string
	Summary   string
	Waiting   bool     // paused on a confirmation
	Busy      bool     // another task is running in this chat
	Notices   []string // things the user must be told regardless of the answer
}

// interrupted reports whether the turn must NOT be written to long-term memory (§4.1):
// «хотел удалить папку…» surfacing later as a fact is worse than forgetting the attempt.
func (r agentResult) interrupted() bool {
	switch r.Status {
	case TaskCancelled, TaskExpired, TaskLost:
		return true
	}
	return r.Waiting || r.Busy
}

// loopState is everything the executor loop needs; it survives a pause in resumeState.
type loopState struct {
	task    *Task
	cfg     Config
	sess    *browser.Session
	msgs    []map[string]any
	sys     string
	packs   []string
	tools   []browser.ToolSpec
	plan    dispatchPlan
	status  StatusFn
	didRead bool
	didSrch bool
	// usedTool: did this task call ANY real tool? request_pack does not count — asking for
	// more hands is not using them.
	usedTool bool
	// wroteSecReport: write_security_report succeeded this task (secops closing gate).
	wroteSecReport bool
	secReportPath  string // last written MD path (for empty-final fallback)
	secReportURL   string // /api/files/… relative
	secReportNudge int    // how many times we already prodded for the MD report
	// seenActs — короткие ярлыки уже сделанного (запрос, URL, файл), чтобы статус
	// «обдумываю» называл, *что* ушло в модель, а не просто «думаю».
	seenActs []string
}

func (ls *loopState) noteBrain(text string) {
	ls.emitStatus(statusBrain(text))
}

func (ls *loopState) emitStatus(u StatusUpdate) {
	if ls == nil || ls.status == nil {
		return
	}
	if strings.TrimSpace(u.Text) == "" {
		return
	}
	ls.status(u)
}

// runLayeredAgent is the whole pipeline for one request: dispatcher → packs → loop → answer.
func runLayeredAgent(req agentRequest) agentResult {
	var notices []string

	// A new task in this chat annuls a hanging confirmation (§6.3 п. 7). Do it BEFORE
	// registering, so the chat never holds two tasks.
	if prev := clearPending(req.actor.ChatID); prev != nil {
		notices = append(notices, "↩️ Отменил ожидание подтверждения на `"+prev.Tool+"` — ты дал новую задачу.")
	}
	// A confirmation that died with the previous process: tell the truth, don't replay (§6.3 п. 10).
	if lost := takeLostPending(req.actor.ChatID); lost != nil {
		notices = append(notices, "🕳 Прошлая задача потерялась при перезапуске (ждала подтверждения на `"+
			lost.Tool+"`) — если она ещё нужна, повтори запрос.")
	}

	t := newTask(req.actor, req.input)
	if err := registerTask(t); err != nil {
		t.Close()
		return agentResult{
			Busy:    true,
			Status:  TaskRunning,
			Text:    agentBusyNotice(activeTask(req.actor.ChatID)),
			Notices: notices,
		}
	}

	// The agent is globally single-threaded (one Chrome session, one artifact buffer). A
	// request from the OTHER channel is not rejected — it queues — but it is told so instead
	// of hanging silently (§4: «Параллельный канал: минимум "занят, в очереди"»).
	if !browserTaskMu.TryLock() {
		if req.status != nil {
			req.status(statusInfo("⏳ Занят другой задачей — встал в очередь."))
		}
		browserTaskMu.Lock()
	}
	locked := true
	unlock := func() {
		if locked {
			locked = false
			browserTaskMu.Unlock()
		}
	}
	defer unlock()
	defer func() {
		// Only remove the registration when the task is NOT parked on a confirmation:
		// a paused task must stay addressable so «да» finds it (§6.3 п. 7).
		if t.GetStatus() != TaskWaitingConfirm {
			unregisterTask(t)
			t.Close()
		}
	}()

	sess := getBrowserSessionWithFFmpeg(req.cfg.FfmpegPath)
	sess.ResetPageAnchor()
	_ = sess.TakeArtifacts() // drop anything a previous task left in the shared buffer

	// ---- Слой 0: есть ли у запроса опора ----
	//
	// Дешевле любого слоя и стоит ДО диспетчера: «найди этот проект» в пустом чате не станет
	// понятнее ни от выбора паков, ни от шести шагов поиска. Проверка детерминированная и
	// срабатывает редко — см. referent.go.
	if phrase, question, need := missingReferent(req.input, t.ChatID); need {
		t.Packs = []string{packChat}
		t.Plan = []string{"переспросить: опоры для «" + phrase + "» в разговоре нет"}
		logTask(t, TaskDone, 0)
		log.Printf("[AGENT] %s: нет опоры для «%s» — переспрашиваю вместо угадывания", t.ID, phrase)
		return agentResult{
			Text:    question,
			Status:  TaskDone,
			TaskID:  t.ID,
			Plan:    t.Plan,
			Notices: notices,
		}
	}

	// ---- Layer 1: dispatcher ----
	if req.status != nil {
		q := oneLine(req.input)
		if q != "" {
			req.status(statusBrain("🧭 разбираю запрос: «" + clipStatus(q, 110) + "»"))
		} else {
			req.status(statusBrain("🧭 разбираю запрос…"))
		}
	}
	plan := runDispatcher(req.cfg, t, req.baseMsgs, req.memSummary, req.hintPacks)
	t.Packs = plan.Packs
	t.Plan = plan.Plan
	t.DispatcherRaw = plan.Raw
	log.Printf("[AGENT] %s packs=%v confirm=%v (%d ms)", t.ID, plan.Packs, plan.Confirm, t.DispatcherMs)

	if req.status != nil {
		if ribbon := describePlan(plan.Plan); ribbon != "" {
			req.status(statusInfo(ribbon))
		}
		if plan.Confirm {
			req.status(statusInfo("ℹ️ Возможно, попрошу подтверждение на одном из шагов."))
		}
	}

	// ---- Layer 2: assemble hands ----
	sys := executorSystemPrompt(plan, t.Actor)
	ls := &loopState{
		task:   t,
		cfg:    req.cfg,
		sess:   sess,
		msgs:   packAgentMessages(req.baseMsgs, sys, req.input),
		sys:    sys,
		packs:  plan.Packs,
		tools:  assemblePackTools(sess, plan.Packs),
		plan:   plan,
		status: req.status,
	}

	res := runExecutorLoop(ls, unlock)
	res.Notices = append(notices, res.Notices...)
	res.Plan = plan.Plan
	res.Summary = plan.Summary
	return res
}

// runExecutorLoop is layer 3 + layer 4. unlockShared releases browserTaskMu — the loop calls
// it BEFORE parking on a confirmation, so /hands, /stop and other chats stay alive (§6.3).
func runExecutorLoop(ls *loopState, unlockShared func()) agentResult {
	t := ls.task

	var lastText string
	stop, refused := false, false
	badArgsRetries := 0

	budget := stepBudget(ls.packs)
	for t.Step < budget && !stop {
		if res, done := checkInterrupted(t); done {
			return res
		}
		step := t.Step
		t.Step++

		if estimateAgentChars(ls.msgs, ls.tools) > agentSoftBudget(ls.cfg) {
			log.Printf("[AGENT] %s: бюджет промпта превышен на шаге %d — сжимаю", t.ID, step)
			ls.msgs = shrinkAgentMsgs(ls.msgs, ls.sys, t.Input, step > 0)
		}

		ls.noteBrain(ls.thinkLine())
		msg, stats, err := llmChatTools(t.Context(), ls.cfg.BrainPort, ls.msgs, ls.tools, 0.3)
		if err != nil && isContextBad(err) {
			log.Printf("[AGENT] %s: контекст/кэш на шаге %d: %v — сжимаю и повторяю", t.ID, step, err)
			ls.msgs = shrinkAgentMsgs(ls.msgs, ls.sys, t.Input, true)
			msg, stats, err = llmChatTools(t.Context(), ls.cfg.BrainPort, ls.msgs, ls.tools, 0.3)
		}
		// Truncated tool-call JSON (long curl+JWT) makes llama-server return HTTP 500.
		// Sanitize any broken Arguments already in history and nudge — do not kill the task.
		if err != nil && isBadToolCallArgs(err) && badArgsRetries < 2 {
			badArgsRetries++
			log.Printf("[AGENT] %s: сломанный JSON tool-call на шаге %d (попытка %d): %s",
				t.ID, step, badArgsRetries, scrubSecrets(err.Error()))
			sanitizeMsgsToolArgs(ls.msgs)
			ls.msgs = append(ls.msgs, map[string]any{"role": "user", "content": badToolArgsNudge})
			continue
		}
		// §3.2: the number to watch is prompt_ms of the FIRST executor turn — that is the
		// once-per-task re-processing of the system prompt, not the dispatcher's own prompt.
		if step == 0 && stats.PromptMs > 0 {
			t.FirstPromptMs = stats.PromptMs
			t.FirstPromptTokens = stats.PromptTokens
			log.Printf("[AGENT] %s: первый ход исполнителя — prompt %d ток за %.0f мс",
				t.ID, stats.PromptTokens, stats.PromptMs)
		}
		if err != nil {
			if res, done := checkInterrupted(t); done {
				return res
			}
			log.Printf("[AGENT] %s: ошибка модели: %v", t.ID, scrubSecrets(err.Error()))
			return finishTask(ls, TaskFailed, "❌ Ошибка модели: "+scrubSecrets(err.Error()))
		}

		calls := sanitizeToolCalls(validToolCalls(msg.ToolCalls))
		if len(calls) == 0 {
			// The model writes ~1 call in 3 as prose instead of tool_calls. Recover it and run
			// it — through the same gate as any other call (see prose_calls.go).
			if recovered := parseProseToolCalls(msg.Content, ls.tools); len(recovered) > 0 {
				log.Printf("[AGENT] %s: восстановил %d вызов(ов) из текста: %s",
					t.ID, len(recovered), toolNamesOf(recovered))
				t.ProseCalls += len(recovered)
				calls = recovered
				// Replace the prose with a proper assistant tool_calls message: the transcript
				// must stay protocol-valid for the next llama-server request (§6.3 п. 5).
				msg.Content = ""
			}
		}
		if len(calls) == 0 {
			// §4.2 anti-hallucination: the nudges live in Go, and the PACK is the switch.
			nudge := ls.nudgeFor(step)
			// A claim of missing capabilities is corrected regardless of pack — it is false in
			// every pack, `chat` included, because request_pack is always advertised.
			if nudge == "" && step < 2 && claimsNoTools(msg.Content) {
				nudge = noToolsNudge
				log.Printf("[AGENT] %s: модель заявила об отсутствии инструментов — поправляю", t.ID)
			}
			if nudge != "" {
				ls.msgs = append(ls.msgs,
					map[string]any{"role": "assistant", "content": strings.TrimSpace(msg.Content)},
					map[string]any{"role": "user", "content": nudge})
				log.Printf("[AGENT] %s: нудж на шаге %d (search=%v read=%v tools=%v)",
					t.ID, step, ls.didSrch, ls.didRead, ls.usedTool)
				continue
			}
			lastText = msg.Content
			ls.noteBrain("✍️ формулирую ответ…")
			break
		}

		msg.ToolCalls = calls
		ls.msgs = append(ls.msgs, assistantToolMsg(msg))

		turn := ls.runTurn(calls)
		if turn.paused && t.GetStatus() == TaskWaitingConfirm {
			return ls.park(unlockShared)
		}
		if turn.stopped {
			stop, refused = true, turn.refused
			lastText = turn.finalText
		}
		ls.msgs = compactToolMessages(ls.msgs)
	}

	// Check the interrupt BEFORE the closing summary pass: a cancelled task must not spend
	// another model round-trip (that is a whole generation the user waits through after
	// pressing /stop).
	if res, done := checkInterrupted(t); done {
		return res
	}
	// A refusal is the final word: no summary pass, no dispatcher summary in front of it.
	if refused {
		return finishTask(ls, TaskFailed, lastText)
	}
	if strings.TrimSpace(lastText) == "" {
		if t.Step >= budget {
			if len(ls.seenActs) > 0 {
				ls.noteBrain("🧮 пишу итог по: " + strings.Join(ls.seenActs, " · "))
			} else {
				ls.noteBrain("🧮 шаги закончились — подвожу итог.")
			}
		}
		lastText = ls.summarize()
		if res, done := checkInterrupted(t); done {
			return res
		}
	}
	// Last line of defence for factual work: the dispatcher routed this to `web`, four nudges
	// went by, and STILL not a single tool ran. Whatever the model wrote is memory, not data.
	//
	// This is not hypothetical politeness — on the live run the model recited a stale answer
	// from the chat history (including an old error note) as if it were fresh research. An
	// honest failure is worth more than a plausible paragraph.
	if !ls.usedTool && hasPack(ls.packs, packWeb) && !refused {
		log.Printf("[AGENT] %s: пак web, но ни один инструмент не отработал — ответ не подтверждён", t.ID)
		return finishTask(ls, TaskFailed,
			"⚠️ Не смог получить данные: ни поиск, ни чтение страниц не отработали "+
				"(сеть или источники недоступны). Ответ из головы давать не буду — повтори запрос, "+
				"и если не заработает, проверь интернет-соединение.")
	}
	// The model sometimes writes the call as PROSE instead of emitting tool_calls — seen on
	// the first live run, on this model+template, for a request that had worked a minute
	// earlier. Nothing ran, so showing that line as the answer would read like a result.
	//
	// Checked HERE, after the summary pass: the prose call can come from either the loop or
	// the closing summary, and on the live run it came from the summary — an earlier check
	// saw an empty lastText and let it through.
	if tool := unparsedToolCall(lastText, ls.tools, ls.usedTool); tool != "" {
		log.Printf("[AGENT] %s: вызов инструмента %s пришёл ТЕКСТОМ, а не tool_call — задача не выполнена", t.ID, tool)
		return finishTask(ls, TaskFailed,
			"⚠️ Модель попыталась вызвать `"+tool+"`, но вызов пришёл текстом, а не машинным tool-call — "+
				"поэтому НИЧЕГО не выполнено. Повтори запрос (обычно со второго раза проходит) "+
				"или сформулируй его иначе.")
	}
	return finishTask(ls, TaskDone, lastText)
}

// turnOutcome is the result of one assistant turn's worth of tool calls.
type turnOutcome struct {
	paused  bool // a confirmation is pending; the task parks after this turn
	stopped bool // leave the loop (hard block / dead tool)
	// refused: the task ends in a refusal, not in an answer. It must NOT be reported as
	// `done`, and the dispatcher's summary must not be pasted in front of it — on the live
	// run a blocked bcdedit answered «запущу bcdedit и покажу вывод ⛔ Не дам это сделать»,
	// which undercuts the refusal it is attached to.
	refused   bool
	finalText string
}

// runTurn executes the tool calls of ONE assistant message.
//
// Read-only independent tools (probe/download/http_get/search…) from one model turn run in
// parallel — that is the cheap speedup for pentest evidence without a second LLM. Mutators
// and browser.act stay serial. Guard/confirm still decide BEFORE any parallel work.
//
// The hard invariant (§6.3 п. 5): every tool_call_id gets a `tool` reply. If call #2 goes to
// a confirmation and #3 is left unanswered, the next llama-server request returns 400 — so
// the rest of the turn receives a synthetic "отложено" instead of nothing. The pending call
// itself is answered later (executed or refused), which keeps the transcript whole either way.
func (ls *loopState) runTurn(calls []toolCall) turnOutcome {
	var out turnOutcome
	deferring := false
	i := 0
	for i < len(calls) {
		if deferring {
			ls.appendToolMsg(calls[i], deferredResult())
			i++
			continue
		}
		// Collect a consecutive batch of parallel-safe tools (capped — live: 20×probe_url
		// in one turn blew past ToolOK quota because all saw count=0 before any bump).
		batch := []toolCall{calls[i]}
		j := i + 1
		if toolParallelOK(calls[i].Function.Name) {
			for j < len(calls) && toolParallelOK(calls[j].Function.Name) && len(batch) < maxParallelTools {
				batch = append(batch, calls[j])
				j++
			}
		}
		if len(batch) == 1 {
			res := ls.executeGuarded(batch[0])
			switch res.control {
			case controlPause:
				deferring = true
				out.paused = true
				i = j
				continue
			case controlStop:
				deferring = true
				out.stopped = true
				out.finalText = res.finalText
				switch res.result.Status {
				case StatusBlocked, StatusDenied:
					out.refused = true
				}
				ls.appendToolMsg(batch[0], res.result)
				i = j
				continue
			}
			ls.appendToolMsg(batch[0], res.result)
			i = j
			continue
		}
		// Parallel batch: pre-check guard serially; only ActionAllow runs together.
		type gated struct {
			tc  toolCall
			out toolOutcome
			run bool // true → execute tool body in parallel
		}
		gatedCalls := make([]gated, len(batch))
		plannedOK := map[string]int{} // квота внутри батча: все 20 probe видели ToolOK=0
		for k, tc := range batch {
			name := tc.Function.Name
			raw := strings.TrimSpace(tc.Function.Arguments)
			args, err := parseToolArgs(raw)
			if err != nil {
				gatedCalls[k] = gated{tc: tc, out: toolOutcome{result: failResult(err.Error(), err)}}
				continue
			}
			ls.task.noteTool(name)
			if name == "request_pack" {
				// request_pack is never in parallelOK, but keep safe.
				gatedCalls[k] = gated{tc: tc, out: ls.executeGuarded(tc)}
				continue
			}
			decision := guardToolCall(ls.task.Actor, name, args)
			fingerprint := toolFingerprint(name, args)
			switch decision.Action {
			case ActionHardBlock, ActionDeny, ActionAsk:
				// Fall back to the full serial path for this one call — keeps pause/deny logic.
				gatedCalls[k] = gated{tc: tc, out: ls.executeGuarded(tc)}
			default:
				if n := ls.task.seenOKCount(fingerprint); n >= 1 {
					line := "⏭ Пропуск повтора: " + toolStatusDetail(name, args)
					ls.emitStatus(statusResult(name, toolArgsBrief(args), line))
					msg := "Повтор того же вызова пропущен — результат уже есть в контексте этой задачи."
					gatedCalls[k] = gated{tc: tc, out: toolOutcome{result: okResult(msg, nil)}}
					continue
				}
				used := ls.task.toolOKCount(name) + plannedOK[name]
				if toolHasOKQuota(name) && used >= toolOKQuota {
					line := "⏭ Квота `" + name + "`"
					ls.emitStatus(statusResult(name, toolArgsBrief(args), line))
					msg := "Инструмент `" + name + "` уже исчерпал квоту успешных вызовов в этой задаче " +
						"(в т.ч. в этом параллельном батче). Дальше — login/download JSON или write_security_report."
					gatedCalls[k] = gated{tc: tc, out: toolOutcome{result: okResult(msg, nil)}}
					continue
				}
				if toolHasOKQuota(name) {
					plannedOK[name]++
				}
				gatedCalls[k] = gated{tc: tc, run: true}
			}
		}
		// If any non-run item paused/stopped, execute remaining runs serially after handling.
		hasControl := false
		for _, g := range gatedCalls {
			if !g.run && (g.out.control == controlPause || g.out.control == controlStop) {
				hasControl = true
				break
			}
		}
		if hasControl {
			for k := range gatedCalls {
				g := &gatedCalls[k]
				if g.run {
					g.out = ls.executeGuarded(g.tc)
				}
				switch g.out.control {
				case controlPause:
					deferring = true
					out.paused = true
				case controlStop:
					deferring = true
					out.stopped = true
					out.finalText = g.out.finalText
					if g.out.result.Status == StatusBlocked || g.out.result.Status == StatusDenied {
						out.refused = true
					}
					ls.appendToolMsg(g.tc, g.out.result)
				default:
					ls.appendToolMsg(g.tc, g.out.result)
				}
				if deferring {
					break
				}
			}
			i = j
			continue
		}

		var wg sync.WaitGroup
		results := make([]toolOutcome, len(gatedCalls))
		nRun := 0
		for _, g := range gatedCalls {
			if g.run {
				nRun++
			}
		}
		ls.emitStatus(statusInfo(fmt.Sprintf("⚡ Параллельно %d инструментов…", nRun)))
		var actMu sync.Mutex // rememberAct / status text order from workers
		for k, g := range gatedCalls {
			if !g.run {
				results[k] = g.out
				continue
			}
			wg.Add(1)
			go func(k int, tc toolCall) {
				defer wg.Done()
				name := tc.Function.Name
				args, _ := parseToolArgs(strings.TrimSpace(tc.Function.Arguments))
				brief := toolArgsBrief(args)
				actMu.Lock()
				ls.emitStatus(statusTool(name, brief, toolStatusDetail(name, args)))
				if hint := toolActHint(name, args); hint != "" {
					ls.rememberAct(hint)
				}
				actMu.Unlock()
				res := ls.runToolQuiet(tc, name, args) // status already emitted
				if res.Status == StatusOK {
					ls.task.bumpSeenOK(toolFingerprint(name, args))
					ls.task.bumpToolOK(name)
				}
				actMu.Lock()
				ls.emitToolResult(name, brief, args, res)
				actMu.Unlock()
				results[k] = toolOutcome{result: res}
			}(k, g.tc)
		}
		wg.Wait()
		for k, g := range gatedCalls {
			res := results[k]
			if res.control == controlStop {
				deferring = true
				out.stopped = true
				out.finalText = res.finalText
			}
			if g.run || res.result.Status != "" || res.result.Text != "" {
				ls.appendToolMsg(g.tc, res.result)
			}
		}
		i = j
	}
	return out
}

// maxParallelTools — сколько read-only вызовов запускать сразу. 20×probe в одном ходе
// обходили ToolOK-квоту и засоряли ленту.
const maxParallelTools = 6

// toolParallelOK — только инструменты со СВОИМ HTTP/диском, без общего Chrome CDP.
// web_search/read_url/browser.* остаются serial: одна Session на процесс.
func toolParallelOK(name string) bool {
	switch name {
	case "probe_url", "download_url", "http_get",
		"github_search", "youtube_transcript", "search_hacker_tools",
		"analyze_log", "scan_text", "audit_file",
		"read_file", "list_dir", "file_info", "media_info":
		return true
	default:
		return false
	}
}

// isPostReportNoise — после write_security_report модель уходила гуглить OWASP вместо итога.
func isPostReportNoise(name string) bool {
	switch name {
	case "web_search", "semantic_search", "read_url", "http_get", "github_search",
		"probe_url", "download_url", "search_hacker_tools", "open_url":
		return true
	default:
		return false
	}
}

// checkInterrupted turns a cancelled/expired context into the user-visible outcome (§4.2).
func checkInterrupted(t *Task) (agentResult, bool) {
	if err := t.Context().Err(); err == nil {
		return agentResult{}, false
	}
	status := TaskCancelled
	if t.Expired() {
		status = TaskExpired
	}
	t.SetStatus(status)
	arts := t.TakeArtifacts()
	logTask(t, status, len(arts))
	return agentResult{
		Text:      statusWord(status),
		Artifacts: arts,
		Status:    status,
		TaskID:    t.ID,
	}, true
}

// finishTask writes the journal line and builds the answer: the dispatcher's summary is the
// first line (§4.1 — otherwise `summary` is a dead field).
func finishTask(ls *loopState, status TaskStatus, text string) agentResult {
	t := ls.task
	t.SetStatus(status)
	arts := t.TakeArtifacts()
	logTask(t, status, len(arts))

	text = strings.TrimSpace(stripThink(text))
	if text == "" {
		// Живой пентест: write_security_report уже записал MD, а модель ушла в OWASP и
		// вернула пустой финал — пользователь видел «итог не вернула» при готовом отчёте.
		if ls != nil && ls.wroteSecReport {
			text = "🛡 Проверка завершена — MD-отчёт записан."
			if ls.secReportPath != "" {
				text += "\nФайл: `" + ls.secReportPath + "`"
			}
			if ls.secReportURL != "" {
				text += "\nСкачать: `/api/files/" + ls.secReportURL + "`"
			}
		} else {
			text = "Готово, но модель не вернула текстовый итог. Повтори запрос конкретнее."
		}
	}
	if s := strings.TrimSpace(ls.plan.Summary); s != "" && status == TaskDone {
		text = s + "\n\n" + text
	}
	text = annotateNumbers(text)
	res := agentResult{
		Text:      text,
		Artifacts: arts,
		Status:    status,
		TaskID:    t.ID,
	}
	if n := t.failedActionsNotice(); n != "" {
		res.Notices = append(res.Notices, n)
	}
	return res
}

// park saves the compacted state and returns the "waiting" answer. The mutex is released
// FIRST: the whole point is that a question does not freeze the agent (§6.3 п. 1–2).
func (ls *loopState) park(unlockShared func()) agentResult {
	t := ls.task
	if unlockShared != nil {
		unlockShared()
	}
	// Waiting on a human must not burn TaskTimeout — swap to a cancel-only context.
	t.armWaitContext()
	p := t.Pending
	rs := &resumeState{
		task:     t,
		cfg:      ls.cfg,
		pending:  p,
		msgs:     compactToolMessages(ls.msgs),
		sys:      ls.sys,
		packs:    ls.packs,
		tools:    ls.tools,
		didRead:  ls.didRead,
		didSrch:  ls.didSrch,
		usedTool: ls.usedTool,
	}
	if prev := savePending(rs); prev != nil && prev.ID != p.ID {
		log.Printf("[PENDING] %s заменил %s", p.ID, prev.ID)
	}
	ls.emitStatus(statusInfo("⏸ Жду подтверждения — руки свободны, можно писать /stop или /hands."))
	return agentResult{
		Text:    p.question(),
		Status:  TaskWaitingConfirm,
		TaskID:  t.ID,
		Waiting: true,
	}
}

// summarize is the no-tools closing pass when the model ended without prose.
func (ls *loopState) summarize() string {
	sumMsgs := slimForSummary(ls.msgs, ls.sys)
	budget := agentSoftBudget(ls.cfg)
	// Trim oldest non-system notes until we fit — NEVER replace with an empty
	// «если данных мало — скажи честно»: на живом пентесте это давало пустой ответ
	// после десятков probe_url, хотя доказательства уже были в контексте.
	for estimateAgentChars(sumMsgs, nil) > budget && len(sumMsgs) > 2 {
		sumMsgs = append(sumMsgs[:1], sumMsgs[2:]...)
	}
	sumMsgs = append(sumMsgs, map[string]any{
		"role": "user",
		"content": "Подведи полный итог по-русски на основе собранных данных выше. " +
			"Укажи источники/URL, что нашёл, как воспроизвести. Ничего не выдумывай. " +
			"Если в данных есть PII/эндпоинты — перечисли их как доказательства.",
	})
	ls.noteBrain("🧠 собираю ответ из того, что нашёл…")
	out, _, err := llmChatTools(ls.task.Context(), ls.cfg.BrainPort, sumMsgs, nil, 0.3)
	if err != nil {
		log.Printf("[AGENT] %s: итоговый проход не удался: %v", ls.task.ID, err)
		return ""
	}
	return out.Content
}

// appendToolMsg writes the `tool` reply for one call. ONLY Status + Text reach the model;
// artifacts are drained into the task for the human (§4.2, §6.3 п. 6).
func (ls *loopState) appendToolMsg(tc toolCall, res ToolResult) {
	name := tc.Function.Name
	ls.msgs = append(ls.msgs, map[string]any{
		"role":         "tool",
		"tool_call_id": tc.ID,
		"name":         name,
		"content":      capAgentText(res.ModelText(), agentMaxToolChars),
	})
	ls.task.AddStep(CompactResult{
		Tool:   name,
		Args:   scrubSecrets(capAgentText(tc.Function.Arguments, 200)),
		Status: string(res.Status),
		Text:   scrubSecrets(capAgentText(res.Text, 400)),
	})
	ls.task.AddArtifacts(res.Artifacts)
	// Провалившееся ДЕЙСТВИЕ фиксируется здесь, а не в тексте для модели: текст она может
	// и проигнорировать — на живом прогоне так и вышло, клик и фокус провалились, а ответ
	// был «Отлично! Чат уже открыт».
	if actionTools[name] && (res.Status == StatusFailed || res.Status == StatusTimeout) {
		ls.task.noteFailedAction(name, res.Text)
	}
	// Drain the shared session buffer after EVERY step, not at the end: with a pending
	// confirmation in flight a neighbouring task would otherwise collect our files.
	ls.task.AddArtifacts(ls.sess.TakeArtifacts())

	switch name {
	case "web_search", "semantic_search", "github_search":
		ls.didSrch = true
		if browser.SearchHasExcerpts(res.Text) {
			ls.didRead = true
		}
	case "read_url", "http_get", "open_url", "get_text":
		ls.didRead = true
	case "write_security_report":
		if res.Status == StatusOK {
			ls.wroteSecReport = true
			if len(res.Artifacts) > 0 {
				ls.secReportPath = res.Artifacts[0]
			}
			if rel := apiFilesRelFromText(res.Text); rel != "" {
				ls.secReportURL = rel
			}
		}
	}
	if name != "request_pack" {
		ls.usedTool = true
	}
	if res.Status == StatusOK {
		rememberToolURLs(ls.task.ChatID, name, res.Text, tc.Function.Arguments)
	}
}

// ===== one tool call, end to end =====

type loopControl int

const (
	controlContinue loopControl = iota
	controlPause                // a confirmation is pending; defer the rest of this turn
	controlStop                 // leave the loop (hard block / dead tool)
)

type toolOutcome struct {
	result    ToolResult
	control   loopControl
	finalText string
}

// executeGuarded is the gate + execution path for one tool call (§6.0).
func (ls *loopState) executeGuarded(tc toolCall) toolOutcome {
	t := ls.task
	name := tc.Function.Name
	raw := strings.TrimSpace(tc.Function.Arguments)

	args, err := parseToolArgs(raw)
	if err != nil {
		return toolOutcome{result: failResult(err.Error(), err)}
	}
	t.noteTool(name)

	// request_pack is always available and never touches the machine: its limits are the
	// escalation counter and the schema budget, not the guard (§4.3).
	if name == "request_pack" {
		esc := requestPack(t, ls.sess, ls.packs, args)
		if esc.Added != "" {
			ls.packs = append(ls.packs, esc.Added)
			ls.tools = assemblePackTools(ls.sess, ls.packs)
			ls.task.Packs = ls.packs
			// Rebuild executor prompt so pack-specific rules (systemPackRules, media, …)
			// appear after escalation — schemas alone are not enough.
			ls.plan.Packs = ls.packs
			ls.sys = executorSystemPrompt(ls.plan, t.Actor)
			if len(ls.msgs) > 0 {
				if role, _ := ls.msgs[0]["role"].(string); role == "system" {
					ls.msgs[0]["content"] = ls.sys
				}
			}
			ls.emitStatus(statusInfo("🧰 Подключил инструменты: " + esc.Added))
		}
		return toolOutcome{result: esc.Result}
	}

	decision := guardToolCall(t.Actor, name, args)
	fingerprint := toolFingerprint(name, args)

	switch decision.Action {
	case ActionHardBlock:
		logHands(t, name, raw, decision, "blocked", string(StatusBlocked), "")
		ls.emitStatus(statusInfo("⛔ Заблокировал: " + decision.Reason))
		return toolOutcome{
			result:    blockedResult(decision.Reason),
			control:   controlStop,
			finalText: "⛔ Не дам это сделать: " + decision.Reason + ". Это заблокировано в любом режиме.",
		}

	case ActionDeny:
		logHands(t, name, raw, decision, "denied", string(StatusDenied), "")
		repeated, deadTool := t.bumpFail(fingerprint, name)
		res := deniedResult(decision.Reason)
		// A refusal never changes its mind inside one task: the SAME call denied twice means
		// the model is looping, so leave honestly instead of burning all 12 steps
		// (приёмка №23). A different call on the same tool still gets its own chance —
		// that is why the counter is keyed by fingerprint (§4.2).
		if repeated || deadTool {
			res.Text += " Ты это уже пробовал — результат тот же."
			return toolOutcome{
				result:    res,
				control:   controlStop,
				finalText: "Не дам это сделать: " + decision.Reason + ". Предложи другой путь или включи полные руки (/hands full).",
			}
		}
		return toolOutcome{result: res}

	case ActionAsk:
		logHands(t, name, raw, decision, "asked", "", "")
		t.Pending = &Pending{
			ID:         newPendingID(t.ID),
			TaskID:     t.ID,
			ChatID:     t.ChatID,
			Channel:    t.Channel,
			Tool:       name,
			Args:       args,
			ToolCallID: tc.ID,
			Reason:     decision.Reason,
			Rule:       decision.Rule,
			Deadline:   time.Now().Add(pendingTimeout),
			CreatedAt:  time.Now(),
		}
		t.SetStatus(TaskWaitingConfirm)
		// The pending call itself gets NO tool message yet: its reply is written on resume
		// (executed or refused), which keeps the transcript complete either way.
		return toolOutcome{result: ToolResult{Status: StatusDeferred}, control: controlPause}
	}

	// После MD-отчёта не уходим гуглить OWASP — сразу итог человеку.
	if ls.wroteSecReport && isPostReportNoise(name) {
		line := "⏭ После отчёта: `" + name + "` не нужен"
		ls.emitStatus(statusResult(name, toolArgsBrief(args), line))
		msg := "MD-отчёт уже записан"
		if ls.secReportURL != "" {
			msg += " (`/api/files/" + ls.secReportURL + "`)"
		}
		msg += ". НЕ вызывай поиск/пробы/скачивания. Дай человеку короткий итог со ссылкой на отчёт — без tool_calls."
		return toolOutcome{result: okResult(msg, nil)}
	}

	// allow — но тот же успешный вызов второй раз не крутим (probe_url-петли на пентесте).
	if n := t.seenOKCount(fingerprint); n >= 1 {
		line := "⏭ Пропуск повтора: " + toolStatusDetail(name, args)
		ls.emitStatus(statusResult(name, toolArgsBrief(args), line))
		msg := "Повтор того же вызова пропущен — результат уже есть в контексте этой задачи.\n" +
			"НЕ вызывай `" + name + "` с теми же аргументами снова.\n" +
			"Следующий шаг: для тел API/файлов — download_url или http_get; финал secops — write_security_report."
		return toolOutcome{result: okResult(msg, nil)}
	}
	// Квота по имени tool: staff/students/roles ×8 и run_command×10 сжигали весь budget.
	if toolHasOKQuota(name) && t.toolOKCount(name) >= toolOKQuota {
		line := "⏭ Квота `" + name + "`: уже " + itoa(toolOKQuota) + "+ успешных в этой задаче"
		ls.emitStatus(statusResult(name, toolArgsBrief(args), line))
		msg := "Инструмент `" + name + "` уже вызывался успешно " + itoa(toolOKQuota) +
			"+ раз в этой задаче — хватит. НЕ долби его снова.\n" +
			"Сделай следующий РАЗНЫЙ шаг или вызови write_security_report с тем, что уже собрано."
		return toolOutcome{result: okResult(msg, nil)}
	}

	res := ls.runTool(tc, name, args)
	exitText := ""
	if res.Err != nil {
		exitText = res.Err.Error()
	}
	logHands(t, name, raw, decision, "auto", string(res.Status), exitText)

	switch res.Status {
	case StatusCancelled:
		return toolOutcome{result: res, control: controlStop}
	case StatusOK:
		t.bumpSeenOK(fingerprint)
		t.bumpToolOK(name)
	case StatusFailed, StatusTimeout:
		repeated, deadTool := t.bumpFail(fingerprint, name)
		if repeated {
			res.Text += "\n(этот же вызов уже был и дал тот же результат — не повторяй его снова)"
		}
		if deadTool {
			// §4.2: a tool that keeps failing ends the loop — but the LOOP, not the answer.
			// finalText here used to become the user's reply verbatim: «Инструмент read_url не
			// работает (…). Дальше без него» — an internal note promising a continuation that
			// never came. Leaving finalText empty sends the task to the summary pass, so the
			// user gets what WAS collected, and the honest note goes to the model instead.
			res.Text += "\nИнструмент `" + name + "` отключён до конца этой задачи (слишком много сбоев). " +
				"Подведи итог по тому, что уже удалось собрать, и честно скажи, чего добыть не вышло."
			log.Printf("[AGENT] %s: инструмент %s отключён после %d сбоев", t.ID, name, toolFailLimit)
			return toolOutcome{result: res, control: controlStop}
		}
	}
	return toolOutcome{result: res}
}

// runTool executes an allowed call: main-package tools first, then the browser session.
// The tool context is clamped to what is left of the task budget (§4.2 hierarchy).
func (ls *loopState) runTool(tc toolCall, name string, args map[string]any) ToolResult {
	brief := toolArgsBrief(args)
	// Без ` · tool` в тексте: Web рисует badge имени, иначе «probe_url probe_url → …».
	ls.emitStatus(statusTool(name, brief, toolStatusDetail(name, args)))
	if hint := toolActHint(name, args); hint != "" {
		ls.rememberAct(hint)
	}
	res := ls.runToolQuiet(tc, name, args)
	ls.emitToolResult(name, brief, args, res)
	return res
}

func (ls *loopState) emitToolResult(name, brief string, args map[string]any, res ToolResult) {
	extra := toolResultLine(name, args, res)
	if extra == "" && strings.TrimSpace(res.Text) == "" {
		return
	}
	summary := extra
	if summary == "" {
		summary = "✔️ готово"
	}
	body := res.Text
	if res.Status == StatusFailed || res.Status == StatusTimeout {
		if body == "" && res.Err != nil {
			body = res.Err.Error()
		}
	}
	ls.emitStatus(statusResultBody(name, brief, summary, body))
}

// runToolQuiet executes without status lines (parallel batch already emitted its own).
func (ls *loopState) runToolQuiet(tc toolCall, name string, args map[string]any) ToolResult {
	t := ls.task
	var res ToolResult
	if localToolNames[name] {
		if local, ok := dispatchLocalTool(t, ls.cfg, name, tc.ID, args); ok {
			res = local
		}
	}
	if res.Status == "" && res.Text == "" && res.Err == nil {
		toolCtx, cancel := context.WithTimeout(t.Context(), toolBudget(t))
		defer cancel()

		log.Printf("[AGENT] %s tool %s(%s)", t.ID, name, capLog(tc.Function.Arguments))
		text, err := ls.sess.Dispatch(toolCtx, name, args)
		res = classifyToolErr(t.Context(), text, err)
		res.Text = capAgentText(res.Text, agentMaxToolChars)
	}
	return res
}

// toolBudget is the per-tool ceiling: never more than what remains of the task (§4.2).
func toolBudget(t *Task) time.Duration {
	remaining := t.remainingBudget()
	if remaining <= 0 {
		return time.Second // let the call fail fast with the task's own deadline
	}
	return remaining
}

// validToolCalls drops calls with an empty name (a known local-model glitch).
func validToolCalls(in []toolCall) []toolCall {
	out := make([]toolCall, 0, len(in))
	for _, tc := range in {
		if strings.TrimSpace(tc.Function.Name) == "" {
			continue
		}
		out = append(out, tc)
	}
	return out
}

// toolNamesOf is for logging a recovered batch.
func toolNamesOf(calls []toolCall) string {
	names := make([]string, 0, len(calls))
	for _, c := range calls {
		names = append(names, c.Function.Name)
	}
	return strings.Join(names, ", ")
}

func hasPack(packs []string, want string) bool {
	for _, p := range packs {
		if p == want {
			return true
		}
	}
	return false
}

// unparsedToolCall reports the tool the model tried to call in prose, or "".
//
// It only fires when the task used NO tool at all: a normal answer may legitimately mention a
// tool name, but an answer that names an active tool with an argument list AND performed
// nothing is the failure mode — the user would otherwise read `run_command(...)` as a result.
func unparsedToolCall(text string, tools []browser.ToolSpec, usedTool bool) string {
	if usedTool || strings.TrimSpace(text) == "" {
		return ""
	}
	for _, tl := range tools {
		name := tl.Function.Name
		if name == "request_pack" {
			continue
		}
		// `name(` preceded by a boundary: line start, space, backtick or quote.
		idx := strings.Index(text, name+"(")
		if idx < 0 {
			continue
		}
		if idx == 0 || strings.ContainsRune(" \n\t`\"'*(", rune(text[idx-1])) {
			return name
		}
	}
	return ""
}

// nudgeFor returns the prod to send when the model tries to finish WITHOUT calling tools, or
// "" to accept the answer. Both branches exist because the first live run produced both
// failures within three requests of each other.
func (ls *loopState) nudgeFor(step int) string {
	if ls.needsWebRead(step) {
		return readNudge(ls.didSrch)
	}
	if ls.needsToolProof(step) {
		return actNudge
	}
	if ls.needsSecurityReport(step) {
		ls.secReportNudge++
		return secReportNudge
	}
	return ""
}

// needsSecurityReport: stress / «сохрани md» without a successful write_security_report.
// Live: model burned 12 steps on curl/catalog, then finished with a fake JSON tool call
// and artifacts=0. One explicit prod before accepting the answer.
func (ls *loopState) needsSecurityReport(step int) bool {
	if ls.wroteSecReport || ls.secReportNudge >= 1 || !hasPack(ls.packs, packSecops) {
		return false
	}
	in := strings.ToLower(ls.task.Input)
	needReport := strings.Contains(in, "write_security_report") ||
		strings.Contains(in, "отчёт") || strings.Contains(in, "отчет") ||
		strings.Contains(in, ".md") || strings.Contains(in, "результат") ||
		strings.Contains(in, "тест на прочность") || strings.Contains(in, "runtime/browser/security") ||
		strings.Contains(in, "пентест") || strings.Contains(in, "pentest")
	return needReport && step >= 1
}

const secReportNudge = "Задача ещё НЕ закрыта: MD-отчёт не записан. Вызови write_security_report " +
	"с target=URL цели и body=полный Markdown (дыры / где / как воспроизвести / как лечить). " +
	"Файлы-доказательства — download_url (не read_url): он кладёт их в runtime/browser/security/evidence " +
	"и даёт ссылку /api/files/… для скачивания в панели. Не пиши итог текстом без этого вызова. " +
	"HTML-оболочка SPA на /admin,/wp-login,/server-status — НЕ доказательство утечки (soft-404)."

func apiFilesRelFromText(s string) string {
	const marker = "/api/files/"
	i := strings.Index(s, marker)
	if i < 0 {
		return ""
	}
	rest := s[i+len(marker):]
	end := len(rest)
	for j, r := range rest {
		if r == ' ' || r == '\n' || r == '\r' || r == '\t' || r == '"' || r == '\'' {
			end = j
			break
		}
	}
	return strings.TrimSpace(rest[:end])
}

// claimsNoTools reports whether the answer tells the user the agent lacks capabilities it
// actually has. This is checked on the FINAL text, because it is the one lie that reaches the
// user fully formed and sounds authoritative:
//
//	«у меня отключены инструменты для работы с интернетом… это требует явного разрешения
//	 на переключение режима» — while request_pack sat right there, needing nobody's permission.
func claimsNoTools(text string) bool {
	t := strings.ToLower(text)
	for _, m := range []string{
		"нет доступа к интернет", "нет доступа к консол", "нет доступа к файл",
		"не имею доступа", "отсутствие доступа", "отключены инструмент", "инструменты отключены",
		"изолированном режиме", "не могу использовать веб", "не могу выйти в интернет",
		"отключен доступ", "нет доступа к внешним", "нет подключенных",
		// Второй живой прогон: на просьбу «сделай скриншот рабочего стола» модель выдала целый
		// реферат о том, чего она не умеет. Каждая строка ниже — цитата из того ответа, и
		// каждая была неправдой уже в момент написания или стала ею с паком system.
		"нет прямого доступа", "нет доступа к вашему компьютер", "нет доступа к рабочему стол",
		"не вижу, что происходит на вашем экран", "не вижу ваш рабочий стол",
		"не могу делать скриншоты рабочего стол", "не могу видеть ваш рабочий стол",
		"не могу управлять мышью", "не могу запускать произвольные приложения",
		"изолированный ии-ассистент", "изолированный ассистент", "не могу взаимодействовать с десктоп",
		"скриншот рабочего стола мне недоступ", "win+shift+s",
	} {
		if strings.Contains(t, m) {
			return true
		}
	}
	// «Остальные наборы инструментов в данный момент не подключены» — the words are far apart,
	// so match the pair rather than a phrase. Requiring BOTH halves keeps an ordinary
	// "модуль не подключен" out of it.
	if strings.Contains(t, "не подключ") || strings.Contains(t, "не активирован") {
		for _, w := range []string{"инструмент", "набор", "пак ", "паки", "web", "browser", "console"} {
			if strings.Contains(t, w) {
				return true
			}
		}
	}
	return false
}

// noToolsNudge is the correction. It names request_pack explicitly, because the failure is not
// unwillingness — the model genuinely believed the packs were a permission system.
const noToolsNudge = "Это неправда: у тебя ЕСТЬ инструмент request_pack, и он подключает любой набор " +
	"(web, browser.read, browser.act, console, files, system, media, trade, secops) БЕЗ разрешения пользователя. " +
	"В частности пак system даёт скриншот НАСТОЯЩЕГО экрана (capture_screen), список окон, клавиатуру, " +
	"мышь, процессы и запуск любых программ — рабочий стол тебе доступен. " +
	"Не рассказывай человеку про свою конфигурацию, не перечисляй ограничения и не предлагай ему " +
	"нажать что-то самому — вызови request_pack с нужным паком и выполни задачу. " +
	"Если после подключения что-то реально не получится — скажешь, что именно не вышло и с какой ошибкой."

// needsToolProof catches the worst failure of the first live run: asked to delete a directory,
// the model called NOTHING and replied «Каталог D:\tmp\kib-probe успешно удален». The
// directory was still there. Security held — no tool ran, so the gate was never involved —
// but the report was false, which is worse than a refusal: the user acts on it.
//
// The rule is the dispatcher's own decision: it routed this task to a pack with real hands, so
// a final answer without a single tool call is a description of work instead of the work.
// One prod is enough; after that the model is allowed to say it cannot.
func (ls *loopState) needsToolProof(step int) bool {
	if ls.usedTool || step >= 2 {
		return false
	}
	for _, p := range ls.packs {
		if p != packChat {
			return true
		}
	}
	return false
}

// actNudge tells the model to DO the thing rather than narrate it, and explicitly allows an
// honest "не смог" — otherwise the cure is another invented success.
const actNudge = "Ты не вызвал ни одного инструмента, но задача требует реального действия. " +
	"НЕ описывай результат так, будто он уже получен: выполни действие через инструмент. " +
	"Если подходящего инструмента нет или действие невозможно — честно напиши, что НЕ сделал, и почему."

// needsWebRead decides whether to prod the model back to the internet instead of letting it
// answer from memory (§4.2).
//
// The PACK is the primary signal, not a keyword list. The dispatcher has already decided this
// task needs the internet; a final answer with nothing read is therefore an answer from the
// model's head. The first live run proved the keyword list alone is not enough: «что там с
// эфиром?» contains no word from any list (no «новост», no «eth», no «цена»), so the nudge
// never fired and the model invented a price of $3450 for an asset trading at $1885 — with
// the front pages of CoinDesk and Forbes offered as «источники».
//
// looksLikeNewsOrResearch stays as a secondary signal for tasks routed to other packs.
func (ls *loopState) needsWebRead(step int) bool {
	if ls.didRead || step >= 4 { // bounded: never nudge forever
		return false
	}
	if hasPack(ls.packs, packWeb) {
		return true
	}
	return shouldForceRead(ls.task.Input, ls.didSrch, ls.didRead, step)
}

// readNudge is the anti-invention prod (§4.2). Kept in Go, not in the prompt, because the
// prompt is what the model ignores when it decides it already knows the news.
func readNudge(didSearch bool) string {
	if !didSearch {
		return "Сначала web_search по запросу пользователя, затем read_url на 3–5 лучших URL из выдачи, " +
			"затем итог со ссылками. Не отвечай без read_url."
	}
	return "Ты ещё НЕ вызвал read_url. Возьми 3–5 конкретных URL из результатов web_search " +
		"(или из блока URL выше) и вызови read_url на каждый. Только после этого — итоговый ответ со ссылками. " +
		"Не проси пользователя прислать ссылки."
}

// ===== resume after a confirmation (§6.3 п. 8) =====

// resumeConfirmed continues a parked task. approved=false records a refusal for the model and
// closes the task; approved=true executes ONLY the confirmed tool and returns to the loop.
// Nothing already done is repeated: run_command is not idempotent, a written file is written.
func resumeConfirmed(rs *resumeState, approved bool, status StatusFn) agentResult {
	t := rs.task
	p := rs.pending

	// /stop cancels the wait context; a burned TaskTimeout must not block resume after park.
	if st := t.GetStatus(); st == TaskCancelled || (t.Context().Err() != nil && st != TaskWaitingConfirm) {
		t.SetStatus(TaskCancelled)
		unregisterTask(t)
		logTask(t, TaskCancelled, 0)
		return agentResult{Text: "⏹ Эта задача уже остановлена.", Status: TaskCancelled, TaskID: t.ID}
	}

	browserTaskMu.Lock()
	locked := true
	unlock := func() {
		if locked {
			locked = false
			browserTaskMu.Unlock()
		}
	}
	defer unlock()
	defer func() {
		if t.GetStatus() != TaskWaitingConfirm {
			unregisterTask(t)
			t.Close()
		}
	}()

	t.armWorkContext()
	t.SetStatus(TaskRunning)
	t.Pending = nil

	ls := &loopState{
		task:     t,
		cfg:      rs.cfg,
		sess:     getBrowserSessionWithFFmpeg(rs.cfg.FfmpegPath),
		msgs:     rs.msgs,
		sys:      rs.sys,
		packs:    rs.packs,
		tools:    rs.tools,
		plan:     dispatchPlan{Packs: rs.packs, Summary: "", Plan: nil},
		status:   status,
		didRead:  rs.didRead,
		didSrch:  rs.didSrch,
		usedTool: rs.usedTool,
	}

	tc := toolCall{ID: p.ToolCallID}
	tc.Function.Name = p.Tool
	if raw, err := json.Marshal(p.Args); err == nil {
		tc.Function.Arguments = string(raw)
	}
	decision := Decision{Action: ActionAsk, Risk: "medium", Rule: p.Rule, Reason: p.Reason}

	if !approved {
		logHands(t, p.Tool, tc.Function.Arguments, decision, "denied", string(StatusDenied), "отказ пользователя")
		ls.appendToolMsg(tc, ToolResult{
			Status: StatusDenied,
			Text:   "пользователь отказал в подтверждении. Не выполняй это действие и не предлагай его снова в этой задаче.",
		})
		t.SetStatus(TaskCancelled)
		arts := t.TakeArtifacts()
		logTask(t, TaskCancelled, len(arts))
		return agentResult{
			Text:      "🚫 Отменил: " + p.Tool + ". Скажи, что сделать вместо этого.",
			Artifacts: arts,
			Status:    TaskCancelled,
			TaskID:    t.ID,
		}
	}

	// Re-classify under the CURRENT hands mode before executing — mode may have flipped
	// while the question was pending.
	actor := t.Actor
	actor.Mode = currentHandsMode()
	t.Actor = actor
	now := guardToolCall(actor, p.Tool, p.Args)
	if now.Action == ActionHardBlock || now.Action == ActionDeny {
		logHands(t, p.Tool, tc.Function.Arguments, now, "denied", string(StatusDenied), "режим изменился после подтверждения")
		ls.appendToolMsg(tc, deniedResult(now.Reason))
		t.SetStatus(TaskCancelled)
		arts := t.TakeArtifacts()
		logTask(t, TaskCancelled, len(arts))
		return agentResult{
			Text:      "🚫 После подтверждения действие уже нельзя: " + now.Reason,
			Artifacts: arts,
			Status:    TaskCancelled,
			TaskID:    t.ID,
		}
	}

	if status != nil {
		status(statusInfo("✅ Подтверждено — выполняю только это действие."))
	}
	res := ls.runTool(tc, p.Tool, p.Args)
	exitText := ""
	if res.Err != nil {
		exitText = res.Err.Error()
	}
	logHands(t, p.Tool, tc.Function.Arguments, decision, "approved", string(res.Status), exitText)
	ls.appendToolMsg(tc, res)
	ls.msgs = compactToolMessages(ls.msgs)

	return runExecutorLoop(ls, unlock)
}

// ===== executor prompt =====

// executorSystemPrompt is built per task from the ACTIVE packs: the model is told about the
// hands it actually has, plus the plan and the URL bag. Short on purpose (§3.2).
//
// Промпт зависит от РЕЖИМА РУК, а не только от паков. Раньше он в любом режиме заканчивался
// строкой «Пиши файлы ТОЛЬКО в рабочие каталоги» — и это была неправда при развязанных руках:
// модель читала своё же ограничение и пересказывала его пользователю как «у меня нет доступа
// к файловой системе». Рубильник должен переключать не только ворота, но и то, что модель о
// себе думает.
func executorSystemPrompt(plan dispatchPlan, actor Actor) string {
	chatID := actor.ChatID
	full := actor.Mode == handsModeFull
	var b strings.Builder
	b.WriteString(agentSystemPrompt())
	b.WriteString(referentRule)
	if hasPack(plan.Packs, packWeb) {
		b.WriteString(webPipelineRules)
	}
	if hasPack(plan.Packs, packBrowserAct) {
		b.WriteString("\n\nПеред кликом/вводом убедись, что страница та же: если инструмент ответил " +
			"«страница сменилась» — перечитай её (get_text/analyze_dom) и повтори действие.\n" +
			"Кликай по ТЕКСТУ: click_element с text=\"надпись на кнопке\". Селектор бери только " +
			"когда надписи нет (иконка, поле ввода). На живых сайтах разметка генерируется, " +
			"угаданный селектор не совпадает ни с чем, и клик просто ждёт таймаут.\n" +
			"НЕ вызывай get_html на большой странице ради поиска селектора — он обрежется и " +
			"упрётся в таймаут. Нужен текст страницы — get_text.")
	}
	if hasPack(plan.Packs, packBrowserRead) || hasPack(plan.Packs, packBrowserAct) {
		// Живой прогон: не сумев подключиться, модель запускала Chrome сама. Второй экземпляр
		// на том же профиле гасит первый — вместе с открытой страницей и незавершённым входом
		// по QR-коду. Пользователь в этот момент как раз пытался авторизоваться.
		b.WriteString("\n\nChrome ЗАПУСКАТЬ САМОМУ ЗАПРЕЩЕНО — ни launch_app, ни run_command. " +
			"Второй экземпляр на том же профиле выключает первый вместе со всеми открытыми " +
			"страницами и недоделанными входами. Если браузер не отвечает — так и скажи " +
			"пользователю и назови ошибку; запускать его будет он.")
	}
	if hasPack(plan.Packs, packTrade) {
		b.WriteString("\n\nЧисла по рынку берутся ТОЛЬКО из analyze_ticker (данные Binance). " +
			"Не бери цифры из головы и не считай их со скриншота.\n" +
			"analyze_ticker возвращает ДВА разбора: скоринг движка и отдельный блок по методике " +
			"Герчика (уровни D1, ATR = High−Low трёх нормальных дней, ЛП/отбой/пробой, стоп и цель). " +
			"Строки «методика запрещает вход» — это отказ, а не предупреждение: пересказывай их " +
			"как есть и не предлагай вход вопреки им. Уровни, стоп и цель бери из блока дословно.\n" +
			"Если analyze_ticker не нашёл тикер на Binance — это НЕ конец работы: монета может " +
			"торговаться на других площадках. Подключи web через request_pack и найди по ней данные " +
			"(что за проект, цена, ликвидность, площадки). Не предлагай пользователю искать самому " +
			"и не предлагай ему скрипты — ищи сам.")
	}
	if hasPack(plan.Packs, packConsole) {
		// On the live run the model wrapped every command in `powershell -Command "…"` inside
		// run_command — which already IS PowerShell. The double quoting broke escaping, output
		// came back as mojibake, and it burned a dozen steps writing helper .ps1 files to work
		// around a problem it had created.
		b.WriteString("\n\nrun_command УЖЕ выполняется в PowerShell (на Windows) или bash. " +
			"НЕ оборачивай команду в `powershell -Command \"...\"`, `pwsh -c` или `cmd /c` — " +
			"пиши команду напрямую: `Get-Date`, `Get-ChildItem D:\\ -Directory`. " +
			"Двойная обёртка ломает экранирование кавычек и портит кириллицу в выводе.\n" +
			"Не создавай временные .ps1-файлы ради одной команды — выполняй её одной строкой.\n" +
			"HTTP API (GET JSON) с Bearer-токеном — http_get(url, authorization), НЕ curl/Invoke-WebRequest " +
			"в run_command: длинный JWT внутри shell-строки ломает JSON tool-call.\n" +
			"Команды, открывающие ОКНА или ждущие ввода, зависают до таймаута и ничего не возвращают: " +
			"`slmgr` без `/dlv`, `msg`, `notepad`, `pause`, `Read-Host`, любые мастера установки. " +
			"Бери консольные аналоги: статус лицензии — `Get-CimInstance SoftwareLicensingProduct | " +
			"Where-Object PartialProductKey | Select-Object Name, LicenseStatus`, а не `slmgr /xpr`.")
	}
	if hasPack(plan.Packs, packSystem) {
		b.WriteString(systemPackRules)
	}
	if hasPack(plan.Packs, packMedia) {
		b.WriteString(mediaPackRules)
	}
	if hasPack(plan.Packs, packSecops) {
		b.WriteString("\n\nSecops: финал задачи — write_security_report(target=URL, body=Markdown). " +
			"Без этого вызова отчёт не считается сданным. " +
			"probe_url — только статус/заголовки/пути, НЕ тело JSON. Утечку API (/staff,/students,…) " +
			"доказывай download_url (файл+кнопка /api/files/…) или http_get; в отчёт перенеси факты. " +
			"Один и тот же URL probe_url'ом не долби — повтор движок пропустит. " +
			"read_url/http_get НЕ сохраняют файл на диск. " +
			"API с JWT — http_get(url, authorization=\"Bearer …\"), не curl в run_command. " +
			"HTML-оболочка SPA на /.env — это soft-404, не утечка: download_url так и скажет.")
	}
	if hasPack(plan.Packs, packFiles) {
		// read_file на PDF возвращает бинарный мусор: файл-то он прочитает, только внутри
		// сжатые потоки, а не текст. Модель после такого пересказывает мусор как содержание.
		b.WriteString("\n\nPDF читается ТОЛЬКО через read_document — он берёт текстовый слой, " +
			"а если внутри скан, распознаёт страницы. read_file на PDF вернёт двоичный мусор.\n" +
			"Документ длинный — в ответе будет выжимка, а полный текст ляжет файлом: если нужной " +
			"точной формулировки (номер, дата, сумма) в приведённом разборе НЕТ, читай этот файл " +
			"read_file'ом, а не пересказывай по памяти. Если ответ в разборе уже есть — просто " +
			"ответь. Копировать текст в новые файлы без просьбы НЕ НАДО: пользователь спросил " +
			"про содержание, а не про файловую работу.\n" +
			"БОЛЬШОЙ документ (справочник, руководство) целиком не пересказывается — в нём ИЩУТ:\n" +
			"- read_document(path, find=\"термин\") — найдёт места и НОМЕРА СТРАНИЦ;\n" +
			"- термин бери НА ЯЗЫКЕ ДОКУМЕНТА (он назван в разборе): в английском руководстве " +
			"ищи fuse, relay, headlight, а не «предохранитель»;\n" +
			"- нашёл нужный раздел — читай его страницы: read_document(path, pages=\"120-125\");\n" +
			"- повторный вызов на том же файле дешёвый (текст берётся из прошлого разбора), " +
			"но вызывать его С ТЕМИ ЖЕ аргументами бессмысленно: меняй find= или pages=.")
	}
	if hasPack(plan.Packs, packFiles) || hasPack(plan.Packs, packConsole) || hasPack(plan.Packs, packSystem) {
		// Without this the model GUESSES the home directory. On the first live run it tried
		// C:\Users\User\ and then C:\Users\kiborg\ — neither existed — and each guess cost the
		// user a confirmation prompt, because an unprovable path is `ask` by §6.1. Telling it
		// the real roots turns those questions back into silent `allow`.
		b.WriteString("\n\nРеальные каталоги этого ПК (бери пути отсюда, не выдумывай имя домашней папки):\n" +
			handsRootsNote())
		if full {
			b.WriteString("\nРуки развязаны: тебе доступна ВСЯ файловая система целиком, любой диск и любая папка. " +
				"Каталоги выше — просто удобные значения по умолчанию, а не граница.")
		} else {
			b.WriteString("\nСейчас руки короткие: запись и удаление ВНЕ этих каталогов спросят подтверждение " +
				"(это не запрет — просто пользователь ответит «да»). Чтение доступно везде.")
		}
	}
	b.WriteString(armouryNote(plan.Packs, actor.Mode))
	b.WriteString(planForPrompt(plan))
	if note := agentURLBagNote(chatID); note != "" {
		b.WriteString("\n\n" + note)
	}
	return b.String()
}

// armouryNote describes the hands. The wording is load-bearing.
//
// The previous version said «Активные наборы инструментов: chat. Нужен ещё один — вызови
// request_pack», and the model read that as a list of DISABLED capabilities. Asked what it
// could do, it recited the internal pack state to the user; asked to look something up, it
// answered «у меня отключены инструменты для работы с интернетом… это требует явного
// разрешения на переключение режима». None of that is true — request_pack is always available
// and needs nobody's permission.
//
// So: never present packs as permissions, always as "already loaded vs one call away", and
// forbid the sentence the model kept producing.
func armouryNote(packs []string, mode string) string {
	hands := "Руки сейчас КОРОТКИЕ (safe): опасные шаги (запись вне рабочих папок, мышь, клавиатура, " +
		"запуск программ) спросят у пользователя «да/нет». Это не запрет — это вопрос. Делай шаг, " +
		"а не отговаривай пользователя."
	if mode == handsModeFull {
		hands = "Руки сейчас ДЛИННЫЕ (full): подтверждений нет, весь ПК твой — любой диск, консоль, " +
			"рабочий стол, окна, мышь, клавиатура, запуск любых программ. Действуй сразу."
	}
	return "\n\n══════════════════════════════════════\n" +
		"ТВОИ РУКИ\n" +
		"══════════════════════════════════════\n" +
		"Схемы, уже загруженные для этой задачи: " + strings.Join(packs, ", ") + ".\n" +
		"Это НЕ предел твоих возможностей. Тебе также доступны:\n" +
		"- web — поиск и чтение страниц в интернете\n" +
		"- browser.read / browser.act — живой Chrome: вкладки, чтение, клики, формы\n" +
		"- console — терминал этого ПК (PowerShell), любые команды\n" +
		"- files — чтение и запись локальных файлов на любом диске\n" +
		"- system — РАБОЧИЙ СТОЛ: скриншот всего экрана или окна, список открытых окон, " +
		"фокус окна, печать текста и нажатие клавиш, мышь, процессы, запуск любых программ, буфер обмена\n" +
		"- media — ВИДЕО и аудио: разобрать ролик, скачать, субтитры, ffmpeg; " +
		"speak_text — озвучить фразу встроенным Qwen3-TTS (женский голос Serena, GPU)\n" +
		"- trade — разбор тикера по Binance, сайзинг, журнал сделок\n" +
		"- secops — логи, IOC, аудит файла, каталог Hacker Tools, probe_url, download_url (файл+ссылка /api/files), MD-отчёт\n" +
		"Любой из них подключается ОДНИМ вызовом request_pack. Разрешение пользователя для этого " +
		"НЕ нужно — не спрашивай его, просто вызови (до 3 раз за задачу).\n" +
		hands + "\n" +
		"ЗАПРЕЩЕНО отвечать, что инструменты «отключены», «не подключены», «недоступны», что у тебя " +
		"«нет доступа к интернету/консоли/рабочему столу/файлам», что ты «не видишь экран», " +
		"«не можешь управлять мышью или клавиатурой», «не можешь запускать программы» или что ты " +
		"«в изолированном режиме». Всё это неправда — перечисленное выше ты умеешь.\n" +
		"Озвучка ответов УЖЕ встроена (Qwen3-TTS, GPU). Если просят «озвучь / голосом / вслух» — " +
		"просто ответь текстом, движок сам прочитает ответ. Отдельный текст — speak_text. " +
		"ЗАПРЕЩЕНО говорить, что TTS нет, и вызывать SAPI / Add-Type System.Speech / ffmpeg ради речи.\n" +
		"ЗАПРЕЩЕНО перечислять пользователю свои ограничения вместо работы и предлагать ему " +
		"сделать что-то руками (Win+Shift+S, «запусти скрипт сам»). Если чего-то реально не вышло — " +
		"скажи, ЧТО именно ты попробовал и какая была ошибка.\n" +
		"Если спрашивают, что ты умеешь, — перечисли весь список выше, а не только загруженные схемы."
}

// referentRule закрывает то, что детерминированная проверка (referent.go) доказать не может:
// история в чате ЕСТЬ, но нужного предмета в ней нет. Формулировка двусторонняя намеренно —
// «спроси, если не понял» без «работай молча, если понял» превращает агента в переспрашивателя,
// а это ровно то поведение, которое отсюда выкорчёвывали.
const referentRule = `

ЕСЛИ В ЗАПРОСЕ УКАЗАНИЕ БЕЗ ПРЕДМЕТА («этот проект», «его», «то видео»), а из разговора
однозначно НЕ видно, о чём речь, — задай ОДИН короткий уточняющий вопрос и остановись.
Искать «что-то похожее» и выдавать это за ответ ЗАПРЕЩЕНО: подборка популярных проектов
вместо запрошенного — это ответ на другой вопрос, даже если в ней есть ссылки на источники.
Если же предмет понятен из разговора или назван в самом запросе — работай молча, без вопросов.`

// mediaPackRules — половина промпта про видео. Главная ошибка, от которой она защищает:
// модель видит в паке download_video и СКАЧИВАЕТ ролик на «разбери это видео», после чего
// отчитывается путём к файлу вместо содержания. Скачивание — это не разбор.
const mediaPackRules = `

══════════════════════════════════════
ВИДЕО И АУДИО (пак media)
══════════════════════════════════════
- «разбери ролик / о чём это видео / вытащи текст / что он рассказывает» → analyze_video.
  download_video — ТОЛЬКО когда просят именно скачать файл. Скачать ≠ разобрать.
- source принимает и путь к файлу, и ссылку. Ссылку скачивать заранее НЕ надо: analyze_video
  сам возьмёт субтитры хостинга, а если их нет — скачает только звук (это в разы быстрее).
- Длина ролика значения не имеет: речь режется на куски и распознаётся по частям. Разбор
  часового видео идёт МИНУТЫ — это нормально, не повторяй вызов и не считай его зависшим.
- В ответе инструмента приходит ВЫЖИМКА, а полная расшифровка лежит файлом по указанному
  пути. Нужны точные формулировки или цитата — прочитай этот файл (пак files, read_file).
- frames= задавай, когда важна КАРТИНКА: слайды, код на экране, интерфейс, «что там
  происходит». Каждый кадр — отдельный запрос к зрению, поэтому 3–6 кадров, а не 12.
- «озвучь / скажи голосом» — speak_text (Qwen3-TTS). Не SAPI, не ffmpeg, не convert_media.
- Если расшифровка пустая или речи нет — так и скажи. Придумывать содержание ролика по
  названию ЗАПРЕЩЕНО.
- Дальше работай с расшифровкой как с обычным текстом: искать по ней проект на GitHub,
  сверять с документацией, объяснять стратегию — это уже обычные web/browser инструменты.`

// systemPackRules is the desktop half of the executor prompt. Каждая строка здесь — это
// ошибка, которую модель делает без неё: снимает вкладку браузера вместо экрана, печатает
// вслепую в окно, которое не в фокусе, или пересказывает пользователю, что скриншот он
// должен сделать сам через Win+Shift+S.
const systemPackRules = `

══════════════════════════════════════
РАБОЧИЙ СТОЛ (пак system)
══════════════════════════════════════
- «скриншот экрана / рабочего стола / что у меня на экране» → capture_screen БЕЗ аргументов.
  Это снимок настоящего экрана Windows. capture_screenshot — это другое, там только вкладка Chrome.
  Файл уходит пользователю автоматически: не проси его прислать скриншот и не советуй Win+Shift+S.
- Скриншот конкретной программы → capture_screen с window="часть заголовка".
- Не знаешь точного заголовка окна → сначала list_windows, потом бери заголовок оттуда.
- ГЛАЗА. Сам по себе снимок ты НЕ видишь — тебе возвращается только путь к файлу. Чтобы
  увидеть экран, добавь look="что найти или понять": тогда инструмент посмотрит на снимок и
  вернёт описание, а для найденного элемента — готовые КООРДИНАТЫ ЭКРАНА для mouse_action.
- Кликать по координатам, взятым из головы, ЗАПРЕЩЕНО. Порядок всегда такой:
  capture_screen(look="…") → взять x,y из строки «НАЙДЕНО» → mouse_action(action="click", x, y)
  → capture_screen(look="что получилось") и проверить, что нажалось нужное.
- Зрение ошибается в координатах на десятки пикселей. Если после клика экран не изменился —
  не повторяй тот же клик: сними заново с look= и уточни точку.
- Печать текста и клавиши идут в АКТИВНОЕ окно. Чтобы не промахнуться, передавай window=
  в сам вызов type_keyboard / press_keys — окно будет сфокусировано перед вводом.
- Запуск программы → launch_app (target="notepad", "D:\\файл.txt", "https://…"), а не run_command:
  launch_app не ждёт завершения, поэтому окно приложения не подвесит задачу.
- Кириллица печатается напрямую через type_keyboard, экранирование не нужно.
- ЗАПРЕЩЕНО чинить непонятную ситуацию закрытием чужих окон и программ. alt+f4, ctrl+w,
  ctrl+q, kill_process, «перезапущу приложение» — только если пользователь ПРЯМО об этом
  попросил. Это его рабочий стол: там несохранённые документы и открытые задачи.
  Не получается — опиши, что именно не вышло, и остановись. Честный отчёт лучше, чем
  инициативное закрытие окна.
- Окно свёрнуто в трей (программа спрятана к часам) — показать его снимком нельзя. Так и
  скажи: «программа свёрнута в трей, открой её, пожалуйста». Не запускай её второй раз.`

// webPipelineRules is the search→read contract, injected only with the `web` pack.
const webPipelineRules = `

══════════════════════════════════════
НОВОСТИ / ФАКТЫ / ЦИФРЫ — обязательный пайплайн
══════════════════════════════════════
a) web_search или semantic_search
b) read_url на 3–5 КОНКРЕТНЫХ URL из выдачи (статьи, не главные страницы)
c) только потом ответ: факты + прямая ссылка на каждый
Запрещено: выдумывать цифры; отвечать «следи за CoinDesk»; заканчивать после одного web_search без read_url.
Если пользователь просит «прочитай/выжимку/подробнее» — бери URL из блока ниже или сделай новый поиск, НЕ проси ссылки у пользователя.
API/JSON endpoint → http_get(url). С авторизацией: http_get(url, authorization="Bearer …"). Не собирай curl в run_command.`

// agentBusyNotice is the answer a parallel request gets (§4: the agent is globally
// single-threaded; a second task in the same chat is queued behind an honest "занят").
func agentBusyNotice(other *Task) string {
	if other == nil {
		return "⏳ Занят другой задачей — дождись ответа или пришли /stop."
	}
	return fmt.Sprintf("⏳ Занят задачей «%s». Дождись ответа или пришли /stop.",
		capAgentText(other.Input, 60))
}
