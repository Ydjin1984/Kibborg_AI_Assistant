package main

// Pack assembly + the schema budget (ТЗ §5, §4.3, приёмка §10 п. 12, 30, 31).

import (
	"strings"
	"testing"

	"kibborg/engine/browser"
)

// TestPackSchemaBudget is the load-bearing one: the assembled schemas must fit 12 000 chars of
// raw JSON, i.e. ~36K after the chat template's ×3 expansion, leaving ~12K of the 48K soft
// budget for the system prompt, history and tool results.
//
// The number is written down on purpose (§5): a "reasonable" threshold gets quietly raised
// until the test is green again, which defeats the point. If this fails, cut schema
// DESCRIPTIONS — do not raise the constant and do not rely on runtime shrinking.
//
// ЧТО ИМЕННО проверяется, изменилось вместе с появлением десятого пака. Раньше это был союз
// ВСЕХ паков сразу — набор, который на самом деле недостижим: диспетчеру §4.1 разрешает 1–3
// пака, а каждая последующая эскалация request_pack уже сверяется с этим же бюджетом перед
// добавлением (§4.3). Зато настоящая дыра — первичный выбор диспетчера — не проверялась
// ничем: модель-маршрутизатор могла перечислить хоть весь список, и исполнитель стартовал бы
// мимо бюджета. Теперь ограничение «не больше трёх» исполняется кодом (maxInitialPacks), а
// тест перебирает ВСЕ сочетания по три и требует, чтобы худшее влезало.
func TestPackSchemaBudget(t *testing.T) {
	sess := browser.New("")
	real := allPacks[1:] // без `chat`: он не добавляет схем

	worstN, worstCombo := 0, []string{}
	for i := 0; i < len(real); i++ {
		for j := i + 1; j < len(real); j++ {
			for k := j + 1; k < len(real); k++ {
				combo := []string{real[i], real[j], real[k]}
				n := schemaChars(assemblePackTools(sess, combo))
				if n > worstN {
					worstN, worstCombo = n, combo
				}
			}
		}
	}
	if worstN > packSchemaBudgetChars {
		t.Fatalf("худшая тройка паков %v = %d символов > лимита %d (после ×3 это %d из %d бюджета). "+
			"Режь описания схем, не порог.", worstCombo, worstN, packSchemaBudgetChars,
			worstN*3, agentSoftBudgetChars)
	}
	t.Logf("худшая тройка %v: %d символов (%.0f%% лимита), после ×3 = %d",
		worstCombo, worstN, float64(worstN)/float64(packSchemaBudgetChars)*100, worstN*3)

	// Диспетчер физически не может выдать больше трёх паков — иначе бюджет выше проверяет не то.
	if got := normalizePacks(allPacks); len(got) != maxInitialPacks {
		t.Fatalf("normalizePacks вернул %d паков (%v); первичный выбор обязан быть ограничен %d",
			len(got), got, maxInitialPacks)
	}

	// request_pack is always advertised, even in `chat`.
	if !hasTool(assemblePackTools(sess, []string{packChat}), "request_pack") {
		t.Error("request_pack должен быть always-on даже в паке chat")
	}
	if len(assemblePackTools(sess, []string{packChat})) != 1 {
		t.Error("пак chat = только request_pack")
	}
}

// Overlapping packs are normal; the assembler must dedup by function name (§5).
func TestPackAssemblyDedups(t *testing.T) {
	sess := browser.New("")
	tools := assemblePackTools(sess, []string{packWeb, packMedia})
	seen := map[string]int{}
	for _, tl := range tools {
		seen[tl.Function.Name]++
	}
	if seen["youtube_transcript"] != 1 {
		t.Fatalf("youtube_transcript есть и в web, и в media — должен остаться один, получили %d",
			seen["youtube_transcript"])
	}
	for name, n := range seen {
		if n > 1 {
			t.Errorf("дубль инструмента %s (%d раз)", name, n)
		}
	}
}

// Every pack must actually deliver the tools §5 promises.
func TestPackContents(t *testing.T) {
	sess := browser.New("")
	want := map[string][]string{
		packWeb:         {"web_search", "semantic_search", "read_url", "http_get", "github_search", "youtube_transcript", "agent_reach"},
		packBrowserRead: {"list_tabs", "open_url", "get_text", "extract_table", "capture_screenshot"},
		packBrowserAct:  {"click_element", "type_text", "submit_form", "upload_file", "drag_element", "close_page"},
		packConsole:     {"run_command"},
		packFiles:       {"read_file", "write_file", "list_dir", "file_info", "mkdir", "delete_path", "read_document"},
		packMedia: {"download_video", "youtube_transcript", "analyze_video", "transcribe_media",
			"video_frames", "media_info", "convert_media"},
		packTrade:  {"analyze_ticker", "size_position", "journal_add", "journal_stats"},
		packSecops: {"analyze_log", "scan_text", "audit_file"},
		packSystem: {"capture_screen", "list_windows", "focus_window", "type_keyboard",
			"press_keys", "mouse_action", "list_processes", "kill_process", "launch_app", "clipboard"},
	}
	for pack, names := range want {
		got := packSpecs(sess, pack)
		for _, n := range names {
			if !hasTool(got, n) {
				t.Errorf("пак %s должен содержать %s", pack, n)
			}
		}
	}
	// The read/act split must be clean: no mutating tool may hide in browser.read.
	for _, tl := range sess.ToolsBrowserRead() {
		if browserActNames[tl.Function.Name] {
			t.Errorf("мутирующий инструмент %s попал в browser.read", tl.Function.Name)
		}
	}
}

var browserActNames = map[string]bool{
	"click_element": true, "type_text": true, "scroll_page": true, "select_option": true,
	"submit_form": true, "upload_file": true, "drag_element": true,
}

// Приёмка №12 + №30: three escalations, then a refusal; the schema budget refuses even earlier.
func TestRequestPackCaps(t *testing.T) {
	sess := browser.New("")
	task := newTask(safeActor(), "тест")
	defer task.Close()

	active := []string{packChat}
	for i, pack := range []string{packFiles, packWeb, packConsole} {
		res := requestPack(task, sess, active, map[string]any{"pack": pack})
		if res.Added != pack {
			t.Fatalf("эскалация %d (%s) не прошла: %s", i+1, pack, res.Result.Text)
		}
		active = append(active, pack)
	}
	if task.Escalations != maxPackEscalations {
		t.Fatalf("счётчик эскалаций = %d, ждали %d", task.Escalations, maxPackEscalations)
	}
	res := requestPack(task, sess, active, map[string]any{"pack": packMedia})
	if res.Added != "" || res.Result.Status != StatusFailed {
		t.Fatalf("четвёртая эскалация должна быть отказом, получили %+v", res)
	}
	if !strings.Contains(res.Result.Text, "лимит паков") {
		t.Errorf("отказ должен объяснять причину, получили: %s", res.Result.Text)
	}
}

// Приёмка №31: request_pack cannot ask for itself or for an already-active pack, and neither
// mistake costs an escalation.
func TestRequestPackRefusesRecursionAndDuplicates(t *testing.T) {
	sess := browser.New("")
	task := newTask(safeActor(), "тест")
	defer task.Close()
	active := []string{packWeb}

	for _, name := range []string{"request_pack", packWeb, "не_существует", "chat"} {
		res := requestPack(task, sess, active, map[string]any{"pack": name})
		if res.Added != "" {
			t.Errorf("%q не должен подключаться", name)
		}
		if res.Result.Status != StatusFailed {
			t.Errorf("%q должен вернуть ошибку модели, получили %s", name, res.Result.Status)
		}
		if task.Escalations != 0 {
			t.Fatalf("%q потратил эскалацию (%d) — не должен", name, task.Escalations)
		}
	}
}

// The budget cap must fire BEFORE the counter (приёмка №30): running out of context is a
// harder wall than running out of tries.
func TestRequestPackBudgetBeatsCounter(t *testing.T) {
	sess := browser.New("")
	task := newTask(safeActor(), "тест")
	defer task.Close()

	// Pretend the schema budget is exhausted by making the candidate set the whole catalog
	// while the counter is still at zero.
	active := []string{packWeb, packBrowserRead, packBrowserAct, packConsole, packFiles, packMedia, packTrade}
	if schemaChars(assemblePackTools(sess, append(active, packSecops))) > packSchemaBudgetChars {
		res := requestPack(task, sess, active, map[string]any{"pack": packSecops})
		if !strings.Contains(res.Result.Text, "бюджет схем") {
			t.Fatalf("ждали отказ по бюджету схем, получили: %s", res.Result.Text)
		}
		if task.Escalations != 0 {
			t.Error("отказ по бюджету не должен тратить эскалацию")
		}
	} else {
		t.Skip("текущие схемы влезают целиком — проверять нечего (это хорошо)")
	}
}

func TestNormalizePacksTolerantAndWhitelisted(t *testing.T) {
	got := normalizePacks([]string{"WEB", "browser", "shell", "мусор", "web"})
	want := []string{packWeb, packBrowserRead, packConsole}
	if len(got) != len(want) {
		t.Fatalf("normalizePacks = %v, ждали %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("normalizePacks = %v, ждали %v", got, want)
		}
	}
	if p := normalizePacks(nil); len(p) != 1 || p[0] != packChat {
		t.Fatalf("пустой список должен давать chat, получили %v", p)
	}
}

func hasTool(tools []browser.ToolSpec, name string) bool {
	for _, tl := range tools {
		if tl.Function.Name == name {
			return true
		}
	}
	return false
}
