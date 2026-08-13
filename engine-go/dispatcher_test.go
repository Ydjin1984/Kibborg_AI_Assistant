package main

// Dispatcher contract + fallback (ТЗ §4.1, приёмка §10 п. 1, 2, 3, 4, 9, 25).
// The LLM call itself needs a live brain, so what is tested here is everything around it:
// JSON parsing, pack validation, and the heuristics that take over when the model breaks.

import (
	"strings"
	"testing"
)

func TestParseDispatchJSON(t *testing.T) {
	cases := []struct {
		name  string
		raw   string
		packs []string
		ok    bool
	}{
		{"чистый JSON", `{"packs":["web"],"plan":["найти"],"confirm":false,"summary":"найду"}`, []string{packWeb}, true},
		{"в заборе", "```json\n{\"packs\":[\"trade\"],\"plan\":[],\"confirm\":false,\"summary\":\"разберу\"}\n```", []string{packTrade}, true},
		{"с болтовнёй вокруг", `Вот ответ: {"packs":["files","console"],"summary":"сделаю"} — готово`, []string{packFiles, packConsole}, true},
		{"confirm строкой", `{"packs":["web"],"confirm":"true","summary":"x"}`, []string{packWeb}, true},
		{"мусорные паки отброшены", `{"packs":["web","телепатия"],"summary":"x"}`, []string{packWeb}, true},
		{"без packs", `{"plan":["шаг"],"summary":"x"}`, nil, false},
		{"не JSON", `конечно, сейчас поищу`, nil, false},
	}
	for _, c := range cases {
		got, ok := parseDispatchJSON(c.raw)
		if ok != c.ok {
			t.Errorf("%s: ok=%v, ждали %v", c.name, ok, c.ok)
			continue
		}
		if !ok {
			continue
		}
		if strings.Join(got.Packs, ",") != strings.Join(c.packs, ",") {
			t.Errorf("%s: packs=%v, ждали %v", c.name, got.Packs, c.packs)
		}
	}
	// confirm as a string must still parse as a bool.
	if p, _ := parseDispatchJSON(`{"packs":["web"],"confirm":"true","summary":"x"}`); !p.Confirm {
		t.Error("confirm:\"true\" должен читаться как true")
	}
}

// Приёмка №1–4: the fallback must NOT drop factual questions into `chat` — answering news
// from the model's head is the exact failure this design exists to prevent (§4.1).
func TestFallbackBiasesToWebNotChat(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"Что сегодня по BTC?", packWeb},
		{"что там с эфиром?", packWeb},
		{"найди свежие статьи про Rust", packWeb},
		{"https://youtu.be/dQw4w9WgXcQ", packMedia},
		{"скачай https://youtu.be/dQw4w9WgXcQ", packMedia},
		{"BTCUSDT", packTrade},
		{"разбери ETH", packTrade},
		{"привет", packChat},
		{"спасибо", packChat},
	}
	for _, c := range cases {
		got := fallbackPlan(c.input, nil)
		if len(got.Packs) == 0 || got.Packs[0] != c.want {
			t.Errorf("fallbackPlan(%q) = %v, ждали %s", c.input, got.Packs, c.want)
		}
	}
}

// A slash command's hint beats the heuristics — the user literally named the capability (§5).
func TestFallbackHonoursHint(t *testing.T) {
	got := fallbackPlan("посмотри что тут", []string{packTrade})
	if got.Packs[0] != packTrade {
		t.Fatalf("подсказка пака проигнорирована: %v", got.Packs)
	}
	// …but a `chat` hint is not a reason to skip the web bias.
	if got := fallbackPlan("что по биткоину", []string{packChat}); got.Packs[0] != packWeb {
		t.Fatalf("подсказка chat не должна ронять фактический запрос в chat: %v", got.Packs)
	}
}

// Приёмка №25: plan[] and summary must reach real consumers, otherwise they are dead fields.
func TestPlanAndSummaryHaveConsumers(t *testing.T) {
	p := dispatchPlan{
		Packs:   []string{packWeb},
		Plan:    []string{"найти 3 статьи", "прочитать", "сохранить выжимку"},
		Summary: "найду статьи и сохраню",
	}
	// 1) plan → executor prompt
	prompt := executorSystemPrompt(p, safeActor())
	for _, step := range p.Plan {
		if !strings.Contains(prompt, step) {
			t.Errorf("шаг %q не попал в промпт исполнителя", step)
		}
	}
	// 2) plan → UI ribbon
	ribbon := describePlan(p.Plan)
	if !strings.Contains(ribbon, "найти 3 статьи") || !strings.Contains(ribbon, "1.") {
		t.Errorf("лента плана для UI собралась неправильно: %q", ribbon)
	}
	// 3) summary → first line of the answer
	ls := &loopState{task: newTask(safeActor(), "x"), plan: p}
	defer ls.task.Close()
	res := finishTask(ls, TaskDone, "Вот что нашёл: …")
	if !strings.HasPrefix(res.Text, p.Summary) {
		t.Errorf("summary должен быть первой строкой ответа, получили: %q", capAgentText(res.Text, 80))
	}
}

// The web pipeline rules must appear ONLY with the web pack — the executor should read rules
// for the hands it actually has (§12 п. 9).
func TestExecutorPromptIsPackAware(t *testing.T) {
	web := executorSystemPrompt(dispatchPlan{Packs: []string{packWeb}}, safeActor())
	if !strings.Contains(web, "read_url") {
		t.Error("с паком web в промпте должен быть пайплайн search→read")
	}
	chat := executorSystemPrompt(dispatchPlan{Packs: []string{packChat}}, safeActor())
	if strings.Contains(chat, "ОБЯЗАТЕЛЬНЫЙ ПАЙПЛАЙН") {
		t.Error("без пака web новостной пайплайн в промпт не идёт")
	}
	act := executorSystemPrompt(dispatchPlan{Packs: []string{packBrowserAct}}, safeActor())
	if !strings.Contains(act, "страница сменилась") {
		t.Error("с browser.act нужно правило про смену вкладки")
	}
	trade := executorSystemPrompt(dispatchPlan{Packs: []string{packTrade}}, safeActor())
	if !strings.Contains(trade, "analyze_ticker") {
		t.Error("с trade нужно правило «числа только из analyze_ticker»")
	}
	sys := executorSystemPrompt(dispatchPlan{Packs: []string{packSystem}}, safeActor())
	if !strings.Contains(sys, "capture_screen") {
		t.Error("с паком system нужно правило про скриншот НАСТОЯЩЕГО экрана")
	}
}

// Промпт обязан меняться вместе с рубильником рук.
//
// Пока он этого не делал, развязанные руки чинили только ворота: модель по-прежнему читала в
// своём же промпте «Пиши файлы ТОЛЬКО в рабочие каталоги» и пересказывала это пользователю
// как «у меня нет доступа к файловой системе». Пользователь при этом видел включённый режим
// full и справедливо считал, что агент не слушается.
func TestExecutorPromptIsHandsAware(t *testing.T) {
	plan := dispatchPlan{Packs: []string{packFiles, packConsole}}
	safe := executorSystemPrompt(plan, safeActor())
	full := executorSystemPrompt(plan, fullActor())

	if safe == full {
		t.Fatal("промпт исполнителя не зависит от режима рук — рубильник до модели не доходит")
	}
	if !strings.Contains(full, "ВСЯ файловая система") {
		t.Error("в длинных руках модели должно быть сказано, что доступен весь диск")
	}
	if strings.Contains(full, "спросят подтверждение") {
		t.Error("в длинных руках подтверждений нет — обещать их промптом нельзя")
	}
	if !strings.Contains(safe, "подтверждение") {
		t.Error("в коротких руках модель должна знать, что опасное спрашивают")
	}
	// И там, и там запрет на «я не могу» обязан оставаться.
	for _, p := range []string{safe, full} {
		if !strings.Contains(p, "ЗАПРЕЩЕНО") {
			t.Error("запрет на рассказы о собственной беспомощности пропал из промпта")
		}
	}
}

// The nudges (§4.2) must survive the refactor — they are the only thing that stops the model
// answering news from memory after a single web_search.
func TestWebNudgesStillLive(t *testing.T) {
	if !shouldForceRead("найди новости по BTC", true, false, 0) {
		t.Fatal("после web_search без read_url нудж обязан сработать")
	}
	if shouldForceRead("найди новости по BTC", true, true, 0) {
		t.Fatal("после read_url нудж не нужен")
	}
	if !strings.Contains(readNudge(true), "read_url") {
		t.Fatal("текст нуджа должен требовать read_url")
	}
	if !strings.Contains(readNudge(false), "web_search") {
		t.Fatal("без поиска нудж должен требовать сначала web_search")
	}
}

// Regression from the FIRST LIVE RUN: «что там с эфиром?» routed to `web` correctly, but the
// keyword heuristic did not recognise it (no «новост», no «eth», no «цена»), so the model
// answered from memory — inventing $3450 for an asset trading at $1885 and citing the front
// pages of CoinDesk and Forbes as sources. The pack, not the wording, must drive the nudge.
func TestWebPackAloneForcesRead(t *testing.T) {
	ls := newTestLoop(t, []string{packWeb})
	ls.task.Input = "что там с эфиром?"

	if !ls.needsWebRead(0) {
		t.Fatal("с паком web финал без чтения недопустим — нудж обязан сработать")
	}
	// The old keyword list on its own would have let this through; that is the bug.
	if looksLikeNewsOrResearch("что там с эфиром?") {
		t.Log("список ключевых слов теперь тоже ловит эту фразу — хорошо, но опираемся не на него")
	}

	// After an actual read the prod stops.
	ls.didRead = true
	if ls.needsWebRead(1) {
		t.Fatal("после read_url нудж не нужен")
	}
	// Bounded: never nudge forever.
	ls.didRead = false
	if ls.needsWebRead(4) {
		t.Fatal("нудж должен быть ограничен по шагам")
	}
	// Without the web pack the old heuristic still decides.
	chat := newTestLoop(t, []string{packChat})
	chat.task.Input = "привет"
	if chat.needsWebRead(0) {
		t.Fatal("приветствие без пака web не должно требовать чтения")
	}
	news := newTestLoop(t, []string{packChat})
	news.task.Input = "найди новости по BTC"
	if !news.needsWebRead(0) {
		t.Fatal("явно новостной запрос ловится и без пака web")
	}
}

// Regression from the FIRST LIVE RUN, the worst one: asked to delete a directory, the model
// called NO tool and answered «Каталог D:\tmp\kib-probe успешно удален». The directory was
// still there. Nothing was executed, so the gate never saw it — the failure is a FALSE REPORT,
// and the user acts on those.
func TestActionPackWithoutToolsIsNudged(t *testing.T) {
	ls := newTestLoop(t, []string{packConsole})
	ls.task.Input = "удали каталог D:\\tmp\\kib-probe целиком"

	nudge := ls.nudgeFor(0)
	if nudge == "" {
		t.Fatal("финал без единого вызова инструмента в паке console обязан получить нудж")
	}
	if !strings.Contains(nudge, "НЕ описывай результат") {
		t.Errorf("нудж должен запрещать описывать несделанное: %q", nudge)
	}
	if !strings.Contains(nudge, "честно напиши, что НЕ сделал") {
		t.Error("нудж обязан разрешать честный отказ — иначе лечение = ещё одна выдумка")
	}

	// After a real tool call the prod stops.
	ls.usedTool = true
	if ls.nudgeFor(1) != "" {
		t.Fatal("после реального вызова инструмента нудж не нужен")
	}
	// Bounded: one prod, then let the model speak.
	ls.usedTool = false
	if ls.nudgeFor(2) != "" {
		t.Fatal("нудж «сделай, а не рассказывай» должен быть однократным")
	}
	// request_pack is not doing the work — it is asking for more hands.
	esc := newTestLoop(t, []string{packFiles})
	esc.runTurn([]toolCall{call("e1", "request_pack", map[string]any{"pack": "console"})})
	if esc.usedTool {
		t.Fatal("request_pack не считается использованием инструмента")
	}
	if esc.nudgeFor(0) == "" {
		t.Fatal("после одной лишь эскалации задача всё ещё ничего не сделала")
	}
	// A chat task legitimately needs nothing.
	chat := newTestLoop(t, []string{packChat})
	if chat.nudgeFor(0) != "" {
		t.Fatal("паку chat инструменты не нужны")
	}
}

func TestDispatcherPromptHasFewShots(t *testing.T) {
	p := dispatcherSystemPrompt()
	// The web↔trade split is the domain's main confusion (§4.1) — it must be shown, not implied.
	for _, want := range []string{"Что сегодня по BTC", "что там с эфиром", "разбери ETH", "залогируй"} {
		if !strings.Contains(p, want) {
			t.Errorf("в промпте диспетчера нет few-shot %q", want)
		}
	}
	for _, pack := range allPacks {
		if !strings.Contains(p, pack) {
			t.Errorf("пак %s не описан в промпте диспетчера", pack)
		}
	}
}

func TestTickerDetection(t *testing.T) {
	yes := []string{"BTC", "eth", "эфир", "BTCUSDT", "solana"}
	for _, s := range yes {
		if !isTickerToken(s) && !knownTickerWords[strings.ToLower(s)] {
			t.Errorf("%q должен опознаваться как тикер", s)
		}
	}
	if looksLikeTickerOnly("разбери подробно эфир с учётом объёмов") {
		t.Error("длинная фраза — не «тикер ± одно слово»")
	}
	if !looksLikeTickerOnly("разбери ETH") {
		t.Error("«разбери ETH» — это тикер ± одно слово")
	}
}
