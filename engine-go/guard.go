package main

// The gate (ТЗ §6). Everything the model wants to do passes through guardToolCall on the
// ACTUAL tool call — not on the dispatcher's routing, which is a convenience layer and not a
// security boundary (§4: `chat` + request_pack("console") is technically possible; the hands
// are cut here and by the channel allowlist).
//
// The decision is a struct, not a word: at debugging time «почему он спросил?» is answered by
// rule+reason in hands.jsonl instead of by reading this file (§6.0).

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// longHexToken collapses opaque hex blobs in shell fingerprints (not JWTs).
var longHexToken = regexp.MustCompile(`(?i)\b[a-f0-9]{32,}\b`)

// jwtLikeCI — same as jwtLike, but case-insensitive: fingerprint lowercases args first.
var jwtLikeCI = regexp.MustCompile(`(?i)eyj[a-z0-9_-]{4,}\.[a-z0-9_-]{4,}\.[a-z0-9_-]{4,}`)

// Action is what the gate decided to do with one tool call.
type Action string

const (
	ActionAllow     Action = "allow"
	ActionAsk       Action = "ask"
	ActionDeny      Action = "deny"
	ActionHardBlock Action = "hard_block"
)

// Rule names (also written to hands.jsonl).
const (
	ruleNone              = ""
	ruleProtectedPath     = "protected_path"
	ruleOutsideAllow      = "outside_allowlist"
	ruleUnparseableCmd    = "unparseable_cmd"
	ruleNuclear           = "nuclear"
	ruleDangerousTool     = "dangerous_tool"
	ruleMoney             = "money"
	ruleControlledExploit = "controlled_exploit"
	rulePackEscalation    = "pack_escalation"
	ruleDesktopInput      = "desktop_input"
	ruleLaunch            = "launch_app"
	ruleCriticalProc      = "critical_process"
)

// Decision is the gate's verdict. Risk/Rule/Reason cost nothing and make the journal useful.
type Decision struct {
	Action Action `json:"action"`
	Risk   string `json:"risk"`   // low | medium | high
	Rule   string `json:"rule"`   // why this branch fired
	Reason string `json:"reason"` // human sentence: «перезапись settings.ini»
}

func allowD() Decision { return Decision{Action: ActionAllow, Risk: "low"} }

func askD(rule, reason string) Decision {
	return Decision{Action: ActionAsk, Risk: "medium", Rule: rule, Reason: reason}
}

func blockD(rule, reason string) Decision {
	return Decision{Action: ActionHardBlock, Risk: "high", Rule: rule, Reason: reason}
}

// moneyTools move real funds. Они не существуют в v1 — политика записана заранее, пока это
// дёшево, чтобы никто «просто не добавил place_order в trade». Подтверждение обязательно в
// ОБОИХ режимах: рубильник рук — про свой ПК, а не про чужой счёт.
var moneyTools = map[string]bool{
	"transfer": true, "withdraw": true, "place_order": true, "cancel_order": true,
}

// dangerousTools are allowed but confirmed in safe mode: they reach outside the machine in
// ways the deterministic checks cannot classify.
var dangerousTools = map[string]bool{
	"agent_reach": true,
}

// desktopInputTools физически двигают мышь и жмут клавиши на живом рабочем столе. В коротких
// руках они спрашивают: `alt+f4` в окне с несохранённым документом или клик по «Удалить» в
// чужом диалоге необратимы, а из текста запроса это не видно. В длинных — идут молча.
var desktopInputTools = map[string]bool{
	"type_keyboard": true, "press_keys": true, "mouse_action": true, "launch_app": true,
}

// guardToolCall is THE gate: actor + tool + args → Decision. Mode is applied last, so the
// journal always records the rule that actually fired, even when `full` waved it through.
//
// РЕЖИМЫ (§6.2, переписано под «короткие руки / длинные руки»):
//
//	safe («короткие»)  — рискованное спрашивает, ядерное отказывает наглухо;
//	full («длинные»)   — рискованное идёт молча, ядерное СПРАШИВАЕТ один раз.
//
// Раньше ядерные команды были hard_block в обоих режимах, то есть существовали действия,
// недостижимые вообще ничем. Теперь недостижимых нет: в длинных руках остаётся ровно один
// предохранитель, и он снимается словом «да». Это предохранитель от ошибки МОДЕЛИ (она может
// выбрать `format` там, где человек просил «почисти диск»), а не запрет пользователю.
func guardToolCall(actor Actor, name string, args map[string]any) Decision {
	d := classifyToolCall(name, args)
	full := actor.Mode == handsModeFull

	// risky фиксируется ДО применения режима: иначе `full` сначала превратил бы ask в allow,
	// а проверка владельца увидела бы уже безобидный вызов и пропустила чужой чат. Рубильник
	// принадлежит владельцу и не раздаёт прав никому другому.
	risky := d.Action != ActionAllow

	if moneyTools[name] {
		d.Action = ActionAsk
		if d.Rule == ruleNone {
			d.Rule = ruleMoney
			d.Reason = "операция с деньгами — подтверждение обязательно в любом режиме"
		}
		return ownerCheck(d, actor, true)
	}
	// sqlmap/hydra/hashcat/bloodhound: always ask, even with hands=full (user policy 2026-08-27).
	if d.Rule == ruleControlledExploit {
		d.Action = ActionAsk
		return ownerCheck(d, actor, true)
	}
	if full && risky {
		if d.Action == ActionHardBlock {
			d.Action = ActionAsk // длинные руки: не «нельзя», а «переспрошу один раз»
		} else {
			d.Action = ActionAllow // ask → allow, deny → allow; rule/reason живут для журнала
		}
	}
	return ownerCheck(d, actor, risky)
}

// ownerCheck denies risky calls that nobody can authorise.
//
// A confirmation needs somebody who may grant it. A non-owner actor (a chat outside the
// allowlist, a non-loopback caller) cannot authorise actions on the owner's machine — so a
// risky call is DENIED, not asked, and the hands switch does not help them either: the switch
// belongs to the owner.
func ownerCheck(d Decision, actor Actor, risky bool) Decision {
	if !risky || actor.IsOwner {
		return d
	}
	d.Action = ActionDeny
	if d.Reason != "" {
		d.Reason += " (запрос не от владельца — подтверждать некому)"
	} else {
		d.Reason = "запрос не от владельца — подтверждать некому"
	}
	return d
}

// classifyToolCall is the mode-independent half of the gate.
func classifyToolCall(name string, args map[string]any) Decision {
	if moneyTools[name] {
		return askD(ruleMoney, "перевод/вывод средств — подтверждение обязательно в любом режиме")
	}
	if dangerousTools[name] {
		return askD(ruleDangerousTool, "внешний CLI agent-reach — действие за пределами ПК")
	}
	if desktopInputTools[name] {
		return askD(ruleDesktopInput, desktopInputReason(name, args))
	}

	switch name {
	case "run_command":
		return guardCommand(argString(args, "command"), argString(args, "cwd"))
	case "write_file":
		return guardPathMutation(argString(args, "path"), "запись файла")
	case "delete_path":
		return guardPathMutation(argString(args, "path"), "удаление")
	case "mkdir":
		return guardPathMutation(argString(args, "path"), "создание каталога")
	case "kill_process":
		return guardKillProcess(args)
	case "convert_media":
		// Без output файл ложится в runtime/media — это наш собственный каталог, спрашивать
		// не о чем. Явный путь проверяется как любая другая запись на диск.
		if out := argString(args, "output"); out != "" {
			return guardPathMutation(out, "запись файла конвертации")
		}
		return allowD()
	}
	// Everything else — reads, searches, browser actions, trade/secops analysis, artifact
	// downloads into runtime/ — is allowed. Reads are `allow` by §6.1.
	return allowD()
}

// desktopInputReason is the human sentence a `system` confirmation carries. It names the
// concrete action, because «подтвердить press_keys» tells the user nothing about what will
// actually happen on their screen.
func desktopInputReason(name string, args map[string]any) string {
	switch name {
	case "type_keyboard":
		return "ввод текста с клавиатуры в активное окно: «" + capAgentText(argString(args, "text"), 80) + "»"
	case "press_keys":
		return "нажатие клавиш на живом рабочем столе: " + argString(args, "keys")
	case "mouse_action":
		return "действие мышью на живом рабочем столе: " + argString(args, "action")
	case "launch_app":
		return "запуск программы: " + argString(args, "target")
	}
	return "действие на рабочем столе: " + name
}

// guardKillProcess singles out the processes whose death takes Windows with them. Killing
// lsass or csrss is an instant BSOD — that is a nuclear action, not a risky one, so in short
// hands it is refused outright and in long hands it asks.
//
// PID-only kills resolve the image name first: otherwise `list_processes` + `kill_process(pid=)`
// bypasses the critical-process map entirely.
func guardKillProcess(args map[string]any) Decision {
	name := strings.ToLower(strings.TrimSpace(argString(args, "name")))
	name = strings.TrimSuffix(name, ".exe")
	pid := int(argFloat(args, "pid"))
	// Always resolve PID when given — never trust caller name alone (name=notepad + pid=lsass).
	if pid > 0 {
		resolved := lookupProcessName(pid)
		if resolved == "" {
			return blockD(ruleCriticalProc,
				fmt.Sprintf("pid %d: имя процесса не определено — завершение без имени запрещено", pid))
		}
		resolved = strings.ToLower(strings.TrimSuffix(resolved, ".exe"))
		if name != "" && name != resolved {
			return blockD(ruleCriticalProc,
				fmt.Sprintf("имя «%s» не совпадает с pid %d (%s) — отказ", name, pid, resolved))
		}
		name = resolved
	}
	if criticalProcesses[name] {
		return blockD(ruleCriticalProc, "процесс "+name+" держит саму Windows — его завершение уронит систему")
	}
	if name == "" && pid <= 0 {
		return askD(ruleDesktopInput, "завершение процесса: не указан ни pid, ни имя")
	}
	target := name
	if target == "" {
		target = fmt.Sprintf("pid %d", pid)
	} else if pid > 0 {
		target = fmt.Sprintf("%s (pid %d)", name, pid)
	}
	return askD(ruleDesktopInput, "завершение процесса "+target)
}

// guardPathMutation implements §6.1's default rule: NOT "if we parsed the path and it is
// outside → ask", but "if we did not PROVE it is inside → ask".
func guardPathMutation(path, what string) Decision {
	if strings.TrimSpace(path) == "" {
		return askD(ruleOutsideAllow, what+": путь не указан — не могу подтвердить, что цель внутри рабочих каталогов")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return askD(ruleOutsideAllow, what+": путь не разбирается — "+path)
	}
	if isProtectedPath(abs) {
		return askD(ruleProtectedPath, what+" защищённого пути: "+abs)
	}
	if !insideHandsRoots(abs) {
		return askD(ruleOutsideAllow, what+" вне рабочих каталогов: "+abs)
	}
	return allowD()
}

// ===== command analysis =====

// guardCommand classifies a shell line. The order matters: nuclear first (it wins over
// everything), then "I cannot even read this" (obfuscation), then path targets.
func guardCommand(command, cwd string) Decision {
	if strings.TrimSpace(command) == "" {
		return allowD() // RunCommand rejects it with a plain error
	}
	norm := normalizeCommand(command)

	if reason, ok := nuclearCommand(norm); ok {
		return blockD(ruleNuclear, "ядерная команда: "+reason)
	}
	// Shell process killers bypass kill_process — classify them here.
	if d, ok := guardShellProcessKill(norm); ok {
		return d
	}
	if tool, ok := controlledExploitCommand(norm); ok {
		return askD(ruleControlledExploit,
			"controlled exploit («"+tool+"») — подтверждение обязательно в любом режиме рук; не часть /stress full без явной просьбы")
	}
	if marker, ok := unparseableCommand(norm); ok {
		return askD(ruleUnparseableCmd, "не могу классифицировать команду ("+marker+")")
	}
	if marker, ok := opaqueScriptInvocation(norm); ok {
		return askD(ruleUnparseableCmd, "непрозрачный скрипт ("+marker+") — не вижу тело команды")
	}

	for _, seg := range commandSegments(norm) {
		verb, mutating := mutatingVerb(seg)
		if !mutating {
			continue
		}
		targets := commandPaths(seg, cwd)
		if len(targets) == 0 {
			return askD(ruleUnparseableCmd,
				"команда изменяет систему ("+verb+"), но цель не разобрал — не могу доказать, что она внутри рабочих каталогов")
		}
		for _, tgt := range targets {
			if isProtectedPath(tgt) {
				return askD(ruleProtectedPath, verb+" по защищённому пути: "+tgt)
			}
			if !insideHandsRoots(tgt) {
				return askD(ruleOutsideAllow, verb+" вне рабочих каталогов: "+tgt)
			}
		}
	}
	return allowD()
}

// normalizeCommand strips PowerShell backticks and quote wrappers, collapses whitespace and
// lowercases — so `R`emove-Item` and "REMOVE-ITEM" are the same string to every check (§6.1).
func normalizeCommand(s string) string {
	s = strings.ReplaceAll(s, "`", "")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ; ")
	s = strings.ReplaceAll(s, "\t", " ")
	s = strings.ToLower(s)
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	return strings.TrimSpace(s)
}

// commandSegments splits a line into individually-executed commands (`;`, `|`, `&&`, `||`).
func commandSegments(norm string) []string {
	repl := strings.NewReplacer("&&", ";", "||", ";", "|", ";", "&", ";")
	parts := strings.Split(repl.Replace(norm), ";")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// controlledExploitExe — offensive / credential / AD collect CLIs. Always ask (even hands=full).
// Matching is on the executable basename inside run_command (sqlmap.cmd → sqlmap).
var controlledExploitExe = map[string]bool{
	"sqlmap": true, "sqlmap.py": true,
	"hydra": true, "thc-hydra": true,
	"hashcat":    true,
	"bloodhound": true, "sharphound": true, "azurehound": true,
}

func controlledExploitCommand(norm string) (string, bool) {
	for _, seg := range commandSegments(norm) {
		fields := strings.Fields(seg)
		if len(fields) == 0 {
			continue
		}
		for _, tok := range fields {
			base := nuclearTok(tok)
			base = strings.TrimSuffix(base, ".cmd")
			base = strings.TrimSuffix(base, ".bat")
			base = strings.TrimSuffix(base, ".exe")
			base = strings.TrimSuffix(base, ".py")
			if controlledExploitExe[base] || controlledExploitExe[base+".py"] {
				return base, true
			}
			// "python …/sqlmap.py" / "python …/jwt_tool.py" — only flag sqlmap path here;
			// jwt_tool is detect-oriented and stays outside this gate.
			if strings.Contains(tok, "sqlmap") {
				return "sqlmap", true
			}
			if strings.Contains(tok, "hashcat") {
				return "hashcat", true
			}
			if strings.Contains(tok, "bloodhound") || strings.Contains(tok, "sharphound") {
				return "bloodhound", true
			}
		}
	}
	return "", false
}

// nuclearExe are commands that are never acceptable — not even in full mode (§6.2).
var nuclearExe = map[string]string{
	"diskpart":         "diskpart (переразметка диска)",
	"bcdedit":          "bcdedit (загрузчик)",
	"format":           "format (форматирование тома)",
	"format-volume":    "Format-Volume (форматирование тома)",
	"clear-disk":       "Clear-Disk (очистка диска)",
	"initialize-disk":  "Initialize-Disk (инициализация диска)",
	"mkfs":             "mkfs (создание ФС)",
	"fdisk":            "fdisk (разметка диска)",
	"reboot":           "перезагрузка ПК",
	"halt":             "остановка ПК",
	"poweroff":         "выключение ПК",
	"shutdown":         "выключение/перезагрузка ПК",
	"stop-computer":    "выключение ПК",
	"restart-computer": "перезагрузка ПК",
}

// nuclearCommand reports whether the line destroys the machine or the disk.
// Scans every segment AND unwraps cmd/powershell wrappers so
// `cmd /c format C:` cannot slip past as a leading "cmd" token.
func nuclearCommand(norm string) (string, bool) {
	for _, seg := range commandSegments(norm) {
		if reason, ok := nuclearInSegment(seg); ok {
			return reason, true
		}
	}
	return "", false
}

func nuclearInSegment(seg string) (string, bool) {
	fields := strings.Fields(seg)
	if len(fields) == 0 {
		return "", false
	}
	chains := [][]string{fields}
	if inner := unwrapShellPayload(fields); len(inner) > 0 {
		chains = append(chains, inner)
	}
	for _, toks := range chains {
		if reason, ok := nuclearTokens(toks); ok {
			return reason, true
		}
	}
	return "", false
}

// unwrapShellPayload returns the command after cmd /c or powershell -Command, if present.
func unwrapShellPayload(fields []string) []string {
	if len(fields) < 2 {
		return nil
	}
	exe := nuclearTok(fields[0])
	switch exe {
	case "cmd":
		for i := 1; i < len(fields)-1; i++ {
			switch fields[i] {
			case "/c", "/k":
				return fields[i+1:]
			}
		}
	case "powershell", "pwsh":
		for i := 1; i < len(fields)-1; i++ {
			switch fields[i] {
			case "-command", "-c", "-encodedcommand", "-enc", "-e", "-en", "-ec", "-encod":
				return fields[i+1:]
			}
		}
	}
	return nil
}

func nuclearTok(tok string) string {
	t := strings.Trim(tok, `"'`)
	t = strings.TrimSuffix(strings.TrimSuffix(t, ".exe"), ".com")
	return filepath.Base(t)
}

func nuclearTokens(fields []string) (string, bool) {
	if len(fields) == 0 {
		return "", false
	}
	// Leading exe (classic path).
	if reason, ok := nuclearExe[nuclearTok(fields[0])]; ok {
		return reason, true
	}
	// Non-leading: wrappers put the real verb later (`cmd /c format C:`).
	for _, tok := range fields[1:] {
		base := nuclearTok(tok)
		if reason, ok := nuclearExe[base]; ok {
			return reason, true
		}
	}
	// Wiping a drive root: `rm -rf /`, `del /f /s /q c:\`, `remove-item c:\ -recurse`.
	for i, tok := range fields {
		if !isDeleteVerb(nuclearTok(tok)) {
			continue
		}
		for _, tgt := range fields[i+1:] {
			if isDriveRoot(strings.Trim(tgt, `"'`)) {
				return "снос корня диска (" + tgt + ")", true
			}
		}
	}
	return "", false
}

func isDeleteVerb(exe string) bool {
	switch exe {
	case "rm", "del", "erase", "rd", "rmdir", "remove-item", "ri":
		return true
	}
	return false
}

// isDriveRoot matches `/`, `/*`, `c:`, `c:\`, `c:/`, `c:\*` — i.e. "the whole disk".
func isDriveRoot(tok string) bool {
	t := strings.TrimSpace(strings.ToLower(tok))
	t = strings.TrimSuffix(t, "*")
	t = strings.TrimSuffix(t, "/")
	t = strings.TrimSuffix(t, `\`)
	if t == "" {
		return tok != "" // "/", "/*", "\" all collapse to "" and all mean the root
	}
	return len(t) == 2 && t[1] == ':' && t[0] >= 'a' && t[0] <= 'z'
}

// guardShellProcessKill catches taskkill / Stop-Process / wmic process delete / kill —
// paths that bypass the kill_process tool and its critical-process map.
func guardShellProcessKill(norm string) (Decision, bool) {
	for _, seg := range commandSegments(norm) {
		fields := strings.Fields(seg)
		if len(fields) == 0 {
			continue
		}
		chains := [][]string{fields}
		if inner := unwrapShellPayload(fields); len(inner) > 0 {
			chains = append(chains, inner)
		}
		for _, toks := range chains {
			if len(toks) == 0 {
				continue
			}
			exe := nuclearTok(toks[0])
			joined := strings.Join(toks, " ")
			critical := shellKillTargetsCritical(joined)
			switch exe {
			case "taskkill", "stop-process", "kill", "pkill", "killall":
				if critical {
					return blockD(ruleCriticalProc, "убийство критического процесса через "+exe), true
				}
				return askD(ruleDesktopInput, "завершение процесса через "+exe), true
			case "wmic":
				if strings.Contains(joined, "process") && strings.Contains(joined, "delete") {
					if critical {
						return blockD(ruleCriticalProc, "убийство критического процесса через wmic"), true
					}
					return askD(ruleDesktopInput, "завершение процесса через wmic"), true
				}
			}
		}
	}
	return Decision{}, false
}

func shellKillTargetsCritical(joined string) bool {
	for name := range criticalProcesses {
		if strings.Contains(joined, name+".exe") || strings.Contains(joined, " "+name+" ") ||
			strings.Contains(joined, "name="+name) || strings.Contains(joined, "name='"+name) ||
			strings.Contains(joined, `name="`+name) || strings.HasSuffix(joined, " "+name) ||
			strings.Contains(joined, "/im "+name) || strings.Contains(joined, "/im \""+name) {
			return true
		}
	}
	return false
}

// opaqueScriptInvocation asks when the real payload is hidden inside a script file.
func opaqueScriptInvocation(norm string) (string, bool) {
	for _, seg := range commandSegments(norm) {
		fields := strings.Fields(seg)
		if len(fields) == 0 {
			continue
		}
		chains := [][]string{fields}
		if inner := unwrapShellPayload(fields); len(inner) > 0 {
			chains = append(chains, inner)
		}
		for _, toks := range chains {
			if len(toks) == 0 {
				continue
			}
			exe := nuclearTok(toks[0])
			switch exe {
			case "powershell", "pwsh":
				for _, t := range toks[1:] {
					switch t {
					case "-file", "-f":
						return "powershell -File", true
					}
				}
			}
			for _, t := range toks {
				low := strings.ToLower(strings.Trim(t, `"'`))
				if strings.HasSuffix(low, ".ps1") || strings.HasSuffix(low, ".bat") ||
					strings.HasSuffix(low, ".cmd") || strings.HasSuffix(low, ".vbs") ||
					strings.HasSuffix(low, ".wsf") {
					return "скрипт " + filepath.Base(low), true
				}
			}
			if exe == "cscript" || exe == "wscript" {
				return exe, true
			}
		}
	}
	return "", false
}

// unparseableMarkers are the forms the deterministic checks cannot read. In safe mode they
// are `ask`, with an honest reason — obfuscation is not a reason to guess (§6.1).
var unparseableMarkers = []string{
	"-encodedcommand", "-enc ", "frombase64string", "invoke-expression",
	"iex ", "iex(", "| iex", ";iex", "downloadstring",
	"downloadfile(", "start-process -verb runas", "certutil -decode", "base64 -d",
	"eval $(", "$(curl", "$(wget",
}

// unparseableCommand reports the first obfuscation marker found. §6.1: in safe mode these are
// `ask` with an honest reason — «не могу классифицировать» beats guessing.
func unparseableCommand(norm string) (string, bool) {
	padded := " " + norm + " "
	for _, m := range unparseableMarkers {
		if strings.Contains(padded, m) {
			return strings.TrimSpace(m), true
		}
	}
	// `powershell -e <base64>` — the short forms of -EncodedCommand. Only treated as
	// obfuscation next to a shell binary, so an unrelated `-e` flag stays auto.
	for _, seg := range commandSegments(norm) {
		fields := strings.Fields(seg)
		if len(fields) < 2 {
			continue
		}
		exe := filepath.Base(strings.TrimSuffix(fields[0], ".exe"))
		if exe != "powershell" && exe != "pwsh" {
			continue
		}
		for _, tok := range fields[1:] {
			switch tok {
			case "-e", "-en", "-enc", "-ec", "-encod":
				return "powershell " + tok, true
			}
		}
	}
	// `curl … | iex`, `irm … | iex` in any spacing.
	if strings.Contains(padded, "|") && (strings.Contains(padded, " iex") || strings.Contains(padded, "invoke-expression")) {
		return "pipe в iex", true
	}
	return "", false
}

// mutatingExe are the verbs that trigger a target check (§6.1). Anything not here is a read
// (Get-Date, git status, go test…) and stays auto.
var mutatingExe = map[string]bool{
	"remove-item": true, "ri": true, "del": true, "erase": true, "rd": true, "rmdir": true,
	"rm": true, "move-item": true, "mi": true, "move": true, "mv": true,
	"set-content": true, "out-file": true, "add-content": true,
	"new-item": true, "ni": true, "copy-item": true, "cpi": true, "copy": true, "cp": true,
	"mkdir": true, "md": true, "touch": true, "tee": true, "tee-object": true,
	"icacls": true, "attrib": true, "takeown": true, "schtasks": true, "sc": true,
	"rename-item": true, "ren": true, "rename": true, "clear-content": true,
}

// mutatingVerb reports whether a segment changes the system, and which verb said so.
func mutatingVerb(seg string) (string, bool) {
	fields := strings.Fields(seg)
	if len(fields) == 0 {
		return "", false
	}
	exe := filepath.Base(strings.TrimSuffix(fields[0], ".exe"))
	if mutatingExe[exe] {
		return exe, true
	}
	// Two-word forms that only mutate with a specific subcommand.
	if len(fields) >= 2 {
		switch exe {
		case "reg":
			if fields[1] == "add" || fields[1] == "delete" || fields[1] == "import" {
				return "reg " + fields[1], true
			}
		case "net":
			if fields[1] == "user" || fields[1] == "localgroup" {
				return "net " + fields[1], true
			}
		}
	}
	// Redirection writes a file even when the exe is a harmless read.
	if strings.Contains(seg, ">") {
		return "> (перенаправление вывода)", true
	}
	return "", false
}

// commandPaths pulls plausible filesystem targets out of a command segment: absolute paths,
// drive-qualified paths, relative paths, and redirect targets. Everything is resolved against
// cwd (or the process directory), because a relative path is only meaningful with a base.
func commandPaths(seg, cwd string) []string {
	base := strings.TrimSpace(cwd)
	if base == "" {
		base, _ = os.Getwd()
	}
	fields := strings.Fields(seg)
	var out []string
	seen := map[string]bool{}
	add := func(tok string) {
		tok = strings.Trim(tok, `"'`)
		tok = strings.TrimSuffix(tok, ",")
		if tok == "" || strings.HasPrefix(tok, "-") || (strings.HasPrefix(tok, "/") && len(tok) <= 3) {
			return // a bare `-Recurse` / `/q` switch, not a path
		}
		if !looksLikePath(tok) {
			return
		}
		abs := tok
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(base, abs)
		}
		abs = filepath.Clean(abs)
		if !seen[abs] {
			seen[abs] = true
			out = append(out, abs)
		}
	}
	for i, f := range fields {
		if f == ">" || f == ">>" {
			if i+1 < len(fields) {
				add(fields[i+1])
			}
			continue
		}
		if idx := strings.Index(f, ">"); idx >= 0 && idx < len(f)-1 {
			add(f[idx+1:])
			continue
		}
		if strings.HasPrefix(f, "-") {
			continue // PowerShell switch; its VALUE is the next field and gets added below
		}
		add(f)
	}
	return out
}

// looksLikePath keeps obvious non-paths (verbs, flags, numbers) out of the target list while
// accepting the forms a model actually writes: D:\tmp\old, ./x, ../y, runtime/logs, name.ext.
func looksLikePath(tok string) bool {
	if tok == "" {
		return false
	}
	if strings.Contains(tok, `:\`) || strings.Contains(tok, ":/") {
		return true
	}
	if strings.HasPrefix(tok, `\\`) || strings.HasPrefix(tok, "/") {
		return true
	}
	if strings.HasPrefix(tok, "./") || strings.HasPrefix(tok, ".\\") ||
		strings.HasPrefix(tok, "../") || strings.HasPrefix(tok, "..\\") {
		return true
	}
	if strings.ContainsAny(tok, `\/`) {
		return true
	}
	// A bare name counts as a path when it has an extension or is a known protected name.
	if strings.Contains(tok, ".") && !strings.HasPrefix(tok, ".") {
		return true
	}
	return tok == ".git" || tok == ".env"
}

// ===== allowlist & protected paths =====

// protectedNames are files that keep the bot itself alive: overwriting settings.ini or
// build.cmd is how the agent breaks its own legs. Protection applies to EVERY mutating tool,
// not only write_file (§6.1) — a project directory being in the allowlist does not make
// `Remove-Item .git -Recurse` automatic.
var protectedNames = map[string]bool{
	"settings.ini": true,
	"start.cmd":    true,
	"stop.cmd":     true,
	"menu.cmd":     true,
	"build.cmd":    true,
	".git":         true,
	".env":         true,
}

// isProtectedPath reports whether abs touches a protected file, the .git tree, or autostart.
func isProtectedPath(abs string) bool {
	clean := filepath.Clean(abs)
	if protectedNames[strings.ToLower(filepath.Base(clean))] {
		return true
	}
	lower := strings.ToLower(clean)
	sep := string(filepath.Separator)
	// Anything INSIDE .git (…\.git\config) is protected too.
	if strings.Contains(lower, sep+".git"+sep) || strings.HasSuffix(lower, sep+".git") {
		return true
	}
	for _, s := range autostartDirs() {
		if s != "" && pathUnderRoot(clean, s) {
			return true
		}
	}
	return false
}

// autostartDirs are the Windows startup folders (persistence locations).
func autostartDirs() []string {
	var out []string
	if appdata := os.Getenv("APPDATA"); appdata != "" {
		out = append(out, filepath.Join(appdata, `Microsoft\Windows\Start Menu\Programs\Startup`))
	}
	if pd := os.Getenv("ProgramData"); pd != "" {
		out = append(out, filepath.Join(pd, `Microsoft\Windows\Start Menu\Programs\StartUp`))
	}
	return out
}

// handsRootsExtra holds AGENT_HANDS_ROOTS from settings.ini/env, set once at startup.
var handsRootsExtra []string

// setHandsRoots records the configured extra roots (called from main after loadConfig).
func setHandsRoots(raw string) {
	handsRootsExtra = nil
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == ';' || r == ',' || r == '\n' }) {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		if abs, err := filepath.Abs(p); err == nil {
			handsRootsExtra = append(handsRootsExtra, filepath.Clean(abs))
		}
	}
}

// handsRoots are the directories where writing/deleting needs no confirmation (§6.1):
// the project, the Desktop, engine-go/runtime, plus AGENT_HANDS_ROOTS.
func handsRoots() []string {
	var roots []string
	if wd, err := os.Getwd(); err == nil {
		roots = append(roots, filepath.Clean(wd))               // engine-go
		roots = append(roots, filepath.Clean(filepath.Dir(wd))) // project root
	}
	if home, err := os.UserHomeDir(); err == nil {
		roots = append(roots,
			filepath.Join(home, "Desktop"),
			filepath.Join(home, "Рабочий стол"),
			filepath.Join(home, "OneDrive", "Desktop"),
			filepath.Join(home, "OneDrive", "Рабочий стол"),
		)
	}
	roots = append(roots, handsRootsExtra...)
	return roots
}

// handsRootsNote lists the existing working roots for the executor prompt. Only real
// directories are shown: offering a path that does not exist is how the model learns to guess.
func handsRootsNote() string {
	var b strings.Builder
	seen := map[string]bool{}
	for _, r := range handsRoots() {
		if r == "" || seen[r] {
			continue
		}
		seen[r] = true
		if st, err := os.Stat(r); err != nil || !st.IsDir() {
			continue
		}
		b.WriteString("- " + r + "\n")
	}
	if b.Len() == 0 {
		return "- (рабочие каталоги не определены — спроси у пользователя полный путь)"
	}
	return strings.TrimRight(b.String(), "\n")
}

// insideHandsRoots reports whether abs is provably inside one of the working roots.
func insideHandsRoots(abs string) bool {
	clean := filepath.Clean(abs)
	for _, r := range handsRoots() {
		if r == "" {
			continue
		}
		if pathUnderRoot(clean, r) {
			return true
		}
	}
	return false
}

// ===== fingerprint =====

// toolFingerprint identifies one concrete INTENTION: tool + normalized arguments. failCount
// keyed by tool name alone would silence delete_path(B) because delete_path(A) was denied —
// different intentions, wrong behaviour (§4.2).
//
// Live: probe_url(staff) ×8 and near-identical run_command(JWT+Invoke-WebRequest) ×5 burned
// whole secops budgets. URLs and shell commands are normalized so tiny cosmetic diffs
// (trailing slash, JWT re-paste) do not reopen the same intention.
func toolFingerprint(name string, args map[string]any) string {
	if len(args) == 0 {
		return name
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(name)
	for _, k := range keys {
		v := args[k]
		s := ""
		switch t := v.(type) {
		case string:
			s = strings.ToLower(strings.TrimSpace(t))
			switch k {
			case "url", "href", "link", "target":
				s = normalizeURLFingerprint(s)
			case "command", "cmd":
				s = scrubCommandFingerprint(s)
			case "path", "file", "input":
				s = strings.ReplaceAll(s, `/`, `\`)
				s = strings.TrimRight(s, `\`)
			}
		default:
			raw, _ := json.Marshal(t)
			s = strings.ToLower(string(raw))
		}
		fmt.Fprintf(&b, "|%s=%s", k, s)
	}
	return b.String()
}

func normalizeURLFingerprint(raw string) string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		return raw
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return strings.TrimRight(raw, "/")
	}
	u.Fragment = ""
	host := strings.TrimPrefix(u.Host, "www.")
	path := strings.TrimRight(u.EscapedPath(), "/")
	out := u.Scheme + "://" + host + path
	if u.RawQuery != "" {
		out += "?" + u.RawQuery
	}
	return out
}

// scrubCommandFingerprint collapses JWTs / long hex so re-pasted tokens do not create a
// "new" fingerprint for the same Invoke-WebRequest skeleton.
func scrubCommandFingerprint(cmd string) string {
	cmd = jwtLikeCI.ReplaceAllString(cmd, "<jwt>")
	cmd = longHexToken.ReplaceAllString(cmd, "<hex>")
	return strings.Join(strings.Fields(cmd), " ")
}

// argString reads a string argument the same way browser.argStr does (tools receive raw JSON).
func argString(args map[string]any, key string) string {
	if v, ok := args[key]; ok {
		if s, ok := v.(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}
