package main

// Packs (ТЗ §5, §4.3). The model never sees the whole arsenal: the dispatcher names packs,
// layer 2 assembles exactly those schemas plus the always-on request_pack, deduping by
// function name (youtube_transcript legitimately lives in both `web` and `media`).
//
// Two independent caps hold the escalation in check — a counter alone is not enough, because
// three small packs are not the same as three big ones.

import (
	"encoding/json"
	"fmt"
	"strings"

	"kibborg/engine/browser"
)

// Pack names. `chat` is the empty arsenal: only request_pack, so a greeting costs nothing but
// the model can still escalate if the "greeting" turns out to be a real task.
const (
	packChat        = "chat"
	packWeb         = "web"
	packBrowserRead = "browser.read"
	packBrowserAct  = "browser.act"
	packConsole     = "console"
	packFiles       = "files"
	packMedia       = "media"
	packTrade       = "trade"
	packSecops      = "secops"
	packSystem      = "system"
)

// allPacks is the whitelist the dispatcher's output is validated against (§4.1).
var allPacks = []string{
	packChat, packWeb, packBrowserRead, packBrowserAct,
	packConsole, packFiles, packMedia, packTrade, packSecops, packSystem,
}

// maxInitialPacks bounds what the DISPATCHER may hand layer 2 in one go.
//
// Без него бюджет схем проверялся только на эскалациях (request_pack), а первичный выбор паков
// не проверялся вообще: модель-маршрутизатор могла перечислить хоть все паки сразу, и
// исполнитель стартовал бы с промптом мимо бюджета. Три — это ровно то число, которое §4.1
// требует от диспетчера словами («1–3 штуки»); теперь это требование ещё и исполняется.
const maxInitialPacks = 3

// packSchemaBudgetChars caps the RAW json.Marshal size of the assembled schemas, before the
// chat template's ~3× expansion (estimateAgentChars multiplies tool JSON by 3). 12 000 → 36K
// expanded, leaving ~12K of the 48K soft budget for the system prompt, history and results.
// A concrete number on purpose: a "reasonable" threshold gets tuned until the test is green.
const packSchemaBudgetChars = 12_000

// maxPackEscalations is the per-task cap on request_pack (§4.3).
const maxPackEscalations = 3

// validPack reports whether name is a real pack.
func validPack(name string) bool {
	for _, p := range allPacks {
		if p == name {
			return true
		}
	}
	return false
}

// normalizePacks cleans the dispatcher's answer: keeps known names, drops duplicates, and
// falls back to `chat` when nothing survived.
func normalizePacks(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, p := range in {
		p = strings.ToLower(strings.TrimSpace(p))
		// Tolerate the shapes a local model produces: "browser", "browser_read".
		switch p {
		case "browser":
			p = packBrowserRead
		case "browser_read":
			p = packBrowserRead
		case "browser_act":
			p = packBrowserAct
		case "shell", "terminal":
			p = packConsole
		case "file", "fs":
			p = packFiles
		case "video", "видео", "audio", "ffmpeg":
			p = packMedia
		case "trading":
			p = packTrade
		case "security", "sec", "hacker", "pentest", "websec", "stress":
			p = packSecops
		case "desktop", "gui", "os", "screen", "рабочий стол", "экран", "windows":
			p = packSystem
		}
		if !validPack(p) || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	if len(out) == 0 {
		out = []string{packChat}
	}
	// §4.1 говорит диспетчеру «1–3 пака»; здесь это становится правилом, а не пожеланием.
	// Всё сверх трёх — не потеря возможностей: недостающий пак подключается request_pack'ом,
	// и вот ТАМ бюджет схем уже проверяется перед каждым добавлением (§4.3).
	if len(out) > maxInitialPacks {
		out = out[:maxInitialPacks]
	}
	return out
}

// packSpecs returns the schemas of ONE pack.
func packSpecs(sess *browser.Session, pack string) []browser.ToolSpec {
	switch pack {
	case packWeb:
		return sess.ToolsWeb()
	case packBrowserRead:
		return sess.ToolsBrowserRead()
	case packBrowserAct:
		return sess.ToolsBrowserAct()
	case packConsole:
		return sess.ToolsConsole()
	case packFiles:
		// Вторая половина пака живёт в package main: чтение PDF требует poppler, tesseract,
		// зрения и модели — всего того, чего в пакете browser нет.
		return append(sess.ToolsFiles(), docToolSpecs()...)
	case packMedia:
		// Пак собирается из двух половин: скачивание и субтитры живут в browser (там yt-dlp),
		// разбор — в package main (ему нужны ffmpeg, распознавание речи, зрение и модель).
		return append(sess.ToolsMedia(), videoToolSpecs()...)
	case packTrade:
		return tradeToolSpecs()
	case packSecops:
		return secopsToolSpecs()
	case packSystem:
		return systemToolSpecs()
	default: // packChat
		return nil
	}
}

// assemblePackTools builds the tool list advertised to the model: the named packs, deduped,
// plus the always-on request_pack.
func assemblePackTools(sess *browser.Session, packs []string) []browser.ToolSpec {
	var out []browser.ToolSpec
	for _, p := range packs {
		out = append(out, packSpecs(sess, p)...)
	}
	out = append(out, requestPackSpec())
	return browser.DedupTools(out)
}

// schemaChars is the raw JSON size of a tool list — the quantity packSchemaBudgetChars caps.
func schemaChars(tools []browser.ToolSpec) int {
	if len(tools) == 0 {
		return 0
	}
	b, err := json.Marshal(tools)
	if err != nil {
		return 0
	}
	return len(b)
}

// packToolNames lists the tools a pack contributes (used by the dispatcher prompt and tests).
func packToolNames(sess *browser.Session, pack string) []string {
	specs := packSpecs(sess, pack)
	names := make([]string, 0, len(specs))
	for _, s := range specs {
		names = append(names, s.Function.Name)
	}
	return names
}

// ===== request_pack (§4.3) =====

// packEscalationResult is what the model gets back from request_pack.
type packEscalationResult struct {
	Result ToolResult
	Added  string // non-empty when the pack was actually added
}

// requestPack applies one escalation to the task. It refuses on FOUR grounds, each with its
// own message so the model can adapt instead of retrying blindly:
//
//	unknown name        → the model's error, no escalation spent
//	already active      → the model's error, no escalation spent (§4.3)
//	request_pack itself → the model's error, no escalation spent
//	cap / schema budget → escalation refused, "закончи с тем что есть"
func requestPack(t *Task, sess *browser.Session, active []string, args map[string]any) packEscalationResult {
	name := strings.ToLower(strings.TrimSpace(argString(args, "pack")))
	reason := argString(args, "reason")

	if name == "" {
		return packEscalationResult{Result: failResult(
			"нужно имя пака: "+strings.Join(allPacks, ", "), nil)}
	}
	if name == "request_pack" {
		return packEscalationResult{Result: failResult(
			"request_pack не может запросить сам себя — назови настоящий пак: "+strings.Join(allPacks, ", "), nil)}
	}
	if name == packChat {
		return packEscalationResult{Result: failResult(
			"пак «chat» не добавляет инструментов — назови нужный: "+strings.Join(allPacks[1:], ", "), nil)}
	}
	// normalizePacks falls back to `chat` for anything it does not recognise, so that IS the
	// "unknown name" signal here (and unknown names are the model's error, not Dispatch's).
	canonical := normalizePacks([]string{name})[0]
	if canonical == packChat {
		return packEscalationResult{Result: failResult(
			"неизвестный пак «"+name+"». Доступны: "+strings.Join(allPacks[1:], ", "), nil)}
	}
	name = canonical
	for _, a := range active {
		if a == name {
			return packEscalationResult{Result: failResult(
				"пак «"+name+"» уже активен — его инструменты уже доступны, эскалация не потрачена", nil)}
		}
	}

	// Cap 1: the schema budget. Checked BEFORE the counter (приёмка №30) — running out of
	// context is a harder wall than running out of tries.
	candidate := assemblePackTools(sess, append(append([]string{}, active...), name))
	if n := schemaChars(candidate); n > packSchemaBudgetChars {
		return packEscalationResult{Result: failResult(fmt.Sprintf(
			"бюджет схем исчерпан (%d > %d символов) — закончи с тем, что есть", n, packSchemaBudgetChars), nil)}
	}
	// Cap 2: the escalation counter.
	if t.Escalations >= maxPackEscalations {
		return packEscalationResult{Result: failResult(fmt.Sprintf(
			"лимит паков (%d эскалации на задачу) — закончи с тем, что есть", maxPackEscalations), nil)}
	}

	t.Escalations++
	logHands(t, "request_pack", "pack="+name, Decision{
		Action: ActionAllow, Risk: "low", Rule: rulePackEscalation, Reason: reason,
	}, "auto", string(StatusOK), "")

	return packEscalationResult{
		Added: name,
		Result: okResult("пак «"+name+"» подключён, инструменты: "+
			strings.Join(packToolNames(sess, name), ", "), nil),
	}
}

// packsSummaryForPrompt renders the pack table injected into the dispatcher prompt (§4.1).
func packsSummaryForPrompt() string {
	rows := []struct{ name, what string }{
		{packChat, "просто ответить словами, инструменты не нужны"},
		{packWeb, "поиск и чтение интернета: новости, факты, «что сейчас», цены из статей"},
		{packBrowserRead, "живой Chrome: вкладки, открыть URL, прочитать текст/DOM/таблицы, скрин"},
		{packBrowserAct, "живой Chrome: клик, ввод текста, формы, прокрутка, загрузка файла"},
		{packConsole, "терминал ПК (PowerShell/bash)"},
		{packFiles, "локальные файлы: прочитать, записать, список, удалить"},
		{packMedia, "видео и аудио: разобрать ролик (речь в текст, кадры глазами), скачать, субтитры, конвертация и нарезка"},
		{packTrade, "разбор тикера по Binance (скоринг + уровни и сценарий по Герчику), размер позиции, журнал сделок"},
		{packSecops, "логи, IOC, аудит файла, каталог Hacker Tools, HTTP-проба URL, MD-отчёт теста на прочность"},
		{packSystem, "рабочий стол ПК: скриншот экрана, окна, клавиатура, мышь, процессы, запуск программ, буфер обмена"},
	}
	var b strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&b, "- %s — %s\n", r.name, r.what)
	}
	return strings.TrimRight(b.String(), "\n")
}
