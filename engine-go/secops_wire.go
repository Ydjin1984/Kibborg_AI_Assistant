package main

// Security & log-analysis wiring: /logs (analyse a log — the bot's own by default),
// /scan (IOC + threat scan of pasted text), and security audit of uploaded files (hash,
// entropy, type). The heavy lifting is deterministic (package secops); the LLM only adds a
// grounded SOC-analyst interpretation on top, so findings can't be hallucinated.
//
// Strictly DEFENSIVE and read-only: detection, explanation, hardening advice — never attacks.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"kibborg/engine/secops"
)

var (
	logsCommands   = []string{"/logs", "/логи", "/log_analyze"}
	scanCommands   = []string{"/scan", "/скан", "/ioc"}
	auditCommands  = []string{"/audit", "/аудит", "/hash"}
	stressCommands = []string{"/stress", "/прочность", "/websec", "/pentest", "/напрочность"}
)

// secSystemPrompt turns the model into a grounded SOC analyst: it interprets the deterministic
// findings, it does not invent them.
const secSystemPrompt = `Ты — старший аналитик по кибербезопасности (SOC / incident response) внутри Kibborg. Тебе дают ГОТОВЫЙ детерминированный разбор (уровни логов, аномалии, IOC, сигнатуры угроз, аудит файла), посчитанный движком.

Правила:
- Опирайся ТОЛЬКО на предоставленные факты. Не выдумывай IOC, адреса, хеши или выводы, которых нет в данных.
- Дай краткую оценку: что происходит, насколько серьёзно (🟢/🟡/🔴), и что делать — приоритизированные ОБОРОНИТЕЛЬНЫЕ шаги (реагирование, харденинг, чистка, ротация ключей).
- Только защита. Никаких инструкций по атаке/эксплуатации.
- По-русски, компактно, по делу. Оформляй СПИСКАМИ, без markdown-таблиц (они не отображаются в Telegram). Если данных недостаточно для вывода — так и скажи, не додумывай.`

// StressMode is the depth of an authorized strength test (/stress + Web security tab).
type StressMode string

const (
	stressModeLight    StressMode = "light"    // только probe_url → отчёт
	stressModeRequired StressMode = "required" // проба + минимум плейбука
	stressModeFull     StressMode = "full"     // всё доступное: чеклисты, браузер, CLI в PATH
)

// parseStressModeToken maps an explicit mode word to StressMode.
// Unknown tokens return ("", false) so they stay in the focus string.
func parseStressModeToken(s string) (StressMode, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "light", "lite", "лайт", "лёгкий", "легкий", "быстрый":
		return stressModeLight, true
	case "required", "обязательный", "обязат", "standard", "std", "базовый", "normal":
		return stressModeRequired, true
	case "full", "полный", "all", "deep", "всё", "все", "max":
		return stressModeFull, true
	default:
		return "", false
	}
}

// normalizeStressMode accepts API/UI values; empty → required (default).
func normalizeStressMode(s string) StressMode {
	if m, ok := parseStressModeToken(s); ok {
		return m
	}
	return stressModeRequired
}

func stressModeLabel(m StressMode) string {
	switch normalizeStressMode(string(m)) {
	case stressModeLight:
		return "лайт"
	case stressModeFull:
		return "полный"
	default:
		return "обязательный"
	}
}

// stressHintPacks returns initial packs for the chosen depth (≤ maxInitialPacks).
func stressHintPacks(mode StressMode) []string {
	switch normalizeStressMode(string(mode)) {
	case stressModeLight:
		return []string{packSecops}
	case stressModeFull:
		return []string{packSecops, packWeb, packConsole}
	default:
		return []string{packSecops, packWeb, packFiles}
	}
}

// stressAuditTask is the agent brief for /stress and the Web security tab.
// attachment — блок файла с вкладки Кибербезопасность; ставится В НАЧАЛО, иначе тонет
// в «Доп. фокус» и модель его игнорирует (живой прогон: файл был в input, tools его не трогали).
func stressAuditTask(target, extra, attachment string, mode StressMode) string {
	target = strings.TrimSpace(target)
	extra = strings.TrimSpace(extra)
	attachment = strings.TrimSpace(attachment)
	mode = normalizeStressMode(string(mode))
	var b strings.Builder
	if attachment != "" {
		b.WriteString("══════════════════════════════════════\n")
		b.WriteString("⚠ ПРИЛОЖЕНИЕ ПОЛЬЗОВАТЕЛЯ — ОБЯЗАТЕЛЬНО УЧЕСТЬ ПЕРВЫМ\n")
		b.WriteString("══════════════════════════════════════\n")
		b.WriteString(attachment)
		b.WriteString("\n")
		b.WriteString("Если это ТЗ / находки / credentials / доказательство:\n")
		b.WriteString("- опирайся на факты из файла, не игнорируй;\n")
		b.WriteString("- при необходимости read_file/read_document по указанному пути;\n")
		b.WriteString("- не долби probe_url по тому, что в файле уже доказано;\n")
		b.WriteString("- добери только недостающее, затем write_security_report.\n\n")
	}
	b.WriteString("Авторизованный тест на прочность цели ")
	b.WriteString(target)
	b.WriteString(".\n")
	b.WriteString("Режим глубины: ")
	b.WriteString(string(mode))
	b.WriteString(" (")
	b.WriteString(stressModeLabel(mode))
	b.WriteString(").\n")
	b.WriteString("Пользователь подтверждает, что цель его (или есть право на проверку).\n")
	if extra != "" {
		b.WriteString("Доп. фокус: ")
		b.WriteString(extra)
		b.WriteByte('\n')
	}
	b.WriteString(secops.LocalToolsSummary(secops.ProbeLocalTools()))
	b.WriteString(".\n")
	b.WriteString("Не вызывай CLI, которых нет (или «не тот» бинарь) — отметь в отчёте.\n")
	b.WriteString("\n")
	switch mode {
	case stressModeLight:
		b.WriteString(`Режим ЛАЙТ — только быстрая проба:
1) probe_url по цели — детерминированная HTTP-проба.
2) write_security_report(target=URL, body=Markdown) — обязательно до ответа.
Не вызывай search_hacker_tools, browser.*, run_command и сканеры. Не доразведуй сайт вручную.
`)
	case stressModeFull:
		b.WriteString(`Режим ПОЛНЫЙ — максимум detect-only из PATH (см. PLAYBOOK.md):
1) probe_url по цели ОДИН раз (не тело JSON).
2) search_hacker_tools — один-два запроса, не крути каталог.
3) При необходимости UI — browser.read (вкладки/текст/скрин), не долби одни и те же URL.
4) Recon: subfinder→dnsx→naabu|nmap→httpx→katana (что есть в PATH).
5) DAST: nuclei -t hacker-tools/nuclei-templates -rate-limit 50; ffuf/gobuster по SecLists/Discovery/Web-Content/common.txt.
6) API: arjun / kr(kiterunner) / jwt_tool при наличии; JSON с токеном — http_get(url, authorization="Bearer …"); утечки — download_url.
7) Repo/deps при path в scope: gitleaks, semgrep, gosec, trivy. Файлы-доказательства — download_url (+ /api/files/…).
8) НЕ запускай sqlmap/hydra/hashcat/bloodhound без явной просьбы в фокусе (ворота всё равно спросят).
9) write_security_report(target=URL, body=полный Markdown) — ОБЯЗАТЕЛЬНО до ответа. Нет CLI — «не установлен».
`)
	default: // required
		b.WriteString(`Режим ОБЯЗАТЕЛЬНЫЙ — проба + минимум плейбука:
1) probe_url по цели ОДИН раз — статус/заголовки/пути (не тело JSON).
2) search_hacker_tools — один запрос (web/appsec/api), не больше.
3) Чеклист PLAYBOOK.md. Открытые API (/staff,/students,/roles,…) — download_url (доказательство на диск) или http_get; не долби probe_url по кругу.
4) Не гоняй nuclei/nmap/ffuf без явной нужды.
5) write_security_report(target=URL, body=полный Markdown с воспроизведением) — обязательный финал.
`)
	}
	b.WriteString(`
Правила: только оценка и харденинг. Не разрушай данные, не брутфорсь пароли без явной просьбы, не пиши эксплойт-пейлоады.
Логин/пароль в фокусе — это разрешение владельца на авторизованную проверку своей цели.
Если пользователь уже дал находки/URL — сохрани их download_url'ом и сразу пиши отчёт; не перепроверяй одно и то же probe_url десять раз.
write_security_report БЕЗ target=URL — ошибка: всегда передавай target и body.
Скачанные доказательства и отчёт — в ответе дай кликабельные /api/files/… (панель рисует кнопку «скачать»).
Опирайся на hacker-tools/PLAYBOOK.md.
`)
	return b.String()
}

// stressHelpText explains /stress usage (Telegram + Web parity).
func stressHelpText() string {
	return "🛡 **Тест на прочность**\n" +
		"Проверка своего сайта/API на дыры с отчётом Markdown.\n\n" +
		"Глубина (радио / слово в команде):\n" +
		"• `light` / лайт — только HTTP-проба\n" +
		"• `required` / обязательный — проба + чеклист плейбука (по умолчанию)\n" +
		"• `full` / полный — чеклисты + браузер + все CLI из PATH\n\n" +
		"Примеры:\n" +
		"`/stress https://example.com`\n" +
		"`/stress light https://example.com`\n" +
		"`/stress full https://example.com headers cookies`\n\n" +
		"Алиасы: `/прочность`, `/websec`, `/pentest`.\n" +
		secops.LocalToolsSummary(secops.ProbeLocalTools()) + ".\n" +
		"Каталог Awesome-Hacking — ссылки/методики, не установленные тулы.\n" +
		"Отчёты: `runtime/browser/security/`.\n\n" +
		"Хеш файла по-прежнему: пришли документ с подписью `/audit`."
}

// botLogFiles are the engine's own logs, analysed by a bare /logs (self-audit).
var botLogFiles = []string{"engine.err.log", "engine.log", "engine.out.log"}

// handleLogsCommand analyses a log file. With no argument it self-audits the bot's own logs;
// with a name it analyses that file, restricted to the engine directory subtree (no traversal
// to arbitrary system files). Returns the deterministic report plus a grounded LLM read.
func handleLogsCommand(cfg Config, arg string) string {
	arg = strings.TrimSpace(arg)
	var content, source string

	if arg == "" {
		content, source = readOwnLogs()
		if content == "" {
			return "📊 Свои логи пусты или недоступны (`engine.err.log`/`engine.out.log`). " +
				"Укажи файл: `/logs имя.log` (в каталоге движка) или пришли лог файлом с подписью `/logs`.\n" +
				"Журналы агента: `/logs runtime/hands.jsonl` (решения ворот) · `/logs runtime/tasks.jsonl` (задачи)."
		}
	} else {
		path, err := safeEnginePath(arg)
		if err != nil {
			return "❌ " + err.Error()
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "❌ Не прочитал файл: " + redactErr(err)
		}
		content = string(capBytes(data, 2<<20)) // cap at 2 MB to bound memory/time
		source = filepath.Base(path)
	}

	report := secops.AnalyzeLog(content, source)
	det := report.RenderMarkdown()
	return det + secNarration(cfg, det)
}

// handleScanCommand scans pasted text for IOCs and threat signatures.
func handleScanCommand(cfg Config, arg string) string {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return "🛡 **Скан безопасности**\n" +
			"Пришли текст: `/scan <лог / заголовки / подозрительная строка>` — найду IP/домены/URL/хеши и сигнатуры атак " +
			"(SQLi, XSS, path-traversal, инъекции команд, утечки секретов, брутфорс).\n" +
			"Файл можно прислать документом с подписью `/scan` (или `/audit` для хеша и энтропии)."
	}
	rep := secops.ScanText(arg)
	det := rep.RenderMarkdown("Скан текста")
	return det + secNarration(cfg, det)
}

// handleSecFile analyses an uploaded file: always a hash/entropy/type audit, plus an IOC/log
// scan of its content when it's text-ish. mode picks the emphasis ("logs" → log analysis).
func handleSecFile(cfg Config, name string, data []byte, mode string) string {
	audit := secops.AuditFile(name, data)
	var b strings.Builder
	b.WriteString(audit.RenderMarkdown())

	// Text-ish content → run the log analyzer (mode=logs) or an IOC scan.
	if looksTextish(data) {
		content := string(capBytes(data, 2<<20))
		b.WriteString("\n\n")
		if mode == "logs" {
			b.WriteString(secops.AnalyzeLog(content, name).RenderMarkdown())
		} else {
			b.WriteString(secops.ScanText(content).RenderMarkdown("Скан содержимого: " + name))
		}
	}
	det := b.String()
	return det + secNarration(cfg, det)
}

// secNarration returns a grounded SOC-analyst interpretation of the deterministic report,
// or "" if the brain is unavailable. Prefixed for display by the caller.
func secNarration(cfg Config, deterministic string) string {
	if !brainReady(cfg.BrainPort) {
		return ""
	}
	msgs := []map[string]any{
		{"role": "system", "content": secSystemPrompt},
		{"role": "user", "content": "Разбор от детерминированного движка:\n\n" + deterministic + "\n\nДай оценку и оборонительные рекомендации."},
	}
	out, err := llmChat(cfg.BrainPort, msgs, 0.3)
	if err != nil {
		return ""
	}
	out = strings.TrimSpace(stripThink(out))
	if out == "" {
		return ""
	}
	return "\n\n🧠 **Оценка аналитика:**\n" + out
}

// parseSecFileCaption reports whether a document caption asks for a security tool, and which.
func parseSecFileCaption(caption string) (mode string, ok bool) {
	if is, _ := parseCommand(caption, scanCommands); is {
		return "scan", true
	}
	if is, _ := parseCommand(caption, auditCommands); is {
		return "audit", true
	}
	if is, _ := parseCommand(caption, logsCommands); is {
		return "logs", true
	}
	return "", false
}

// handleSecDocument downloads an uploaded document and runs the security analysis on its bytes
// (works for binaries too — hash/entropy). Used by the Telegram document route.
func handleSecDocument(cfg Config, botAPI string, chatID int64, doc *tgDocument, mode string) {
	stop := startTyping(botAPI, chatID)
	defer stop()
	filePath, err := getTelegramFilePath(botAPI, doc.FileID)
	if err != nil {
		sendTelegramMessage(botAPI, chatID, "❌ Не смог получить файл из Telegram: "+redactErr(err))
		return
	}
	data, _, err := downloadTelegramFile(cfg.TelegramToken, filePath)
	if err != nil {
		sendTelegramMessage(botAPI, chatID, "❌ Не смог скачать файл: "+redactErr(err))
		return
	}
	stop()
	sendTelegramMessage(botAPI, chatID, handleSecFile(cfg, doc.FileName, data, mode))
}

// ===== helpers =====

// readOwnLogs concatenates the bot's own log files (most-relevant first) for self-audit.
func readOwnLogs() (content, source string) {
	var parts []string
	var names []string
	for _, f := range botLogFiles {
		if data, err := os.ReadFile(f); err == nil && len(strings.TrimSpace(string(data))) > 0 {
			parts = append(parts, string(capBytes(data, 1<<20)))
			names = append(names, f)
		}
	}
	return strings.Join(parts, "\n"), strings.Join(names, "+")
}

// safeEnginePath resolves name under the engine working directory and refuses anything that
// escapes it (no traversal to arbitrary system files / secrets).
func safeEnginePath(name string) (string, error) {
	root, err := os.Getwd()
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(filepath.Join(root, name))
	if err != nil {
		return "", err
	}
	if abs != root && !strings.HasPrefix(abs, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("путь вне рабочего каталога движка — читаю только логи здесь")
	}
	return abs, nil
}

func capBytes(data []byte, max int) []byte {
	if len(data) > max {
		return data[len(data)-max:] // keep the TAIL — recent log lines matter most
	}
	return data
}

// looksTextish reports whether data is predominantly printable (so it's worth text-scanning).
func looksTextish(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	n := len(data)
	if n > 4096 {
		n = 4096
	}
	printable := 0
	for _, b := range data[:n] {
		if b == '\t' || b == '\n' || b == '\r' || (b >= 0x20 && b < 0x7F) || b >= 0x80 {
			printable++
		}
	}
	return float64(printable)/float64(n) > 0.85
}

// splitStressArg pulls URL/host, optional depth mode, and focus out of `/stress …`.
// Mode words (light/required/full и русские синонимы) выкусываются из любого места.
// Без явного режима — required.
func splitStressArg(arg string) (target, focus string, mode StressMode) {
	mode = stressModeRequired
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return "", "", mode
	}
	fields := strings.Fields(arg)
	if len(fields) == 0 {
		return "", "", mode
	}
	var rest []string
	for _, f := range fields {
		if m, ok := parseStressModeToken(f); ok {
			mode = m
			continue
		}
		rest = append(rest, f)
	}
	if len(rest) == 0 {
		return "", "", mode
	}
	target = rest[0]
	if len(rest) > 1 {
		focus = strings.Join(rest[1:], " ")
	}
	// Accept bare hosts: example.com → https://example.com
	lower := strings.ToLower(target)
	if !strings.Contains(lower, "://") && strings.Contains(target, ".") &&
		!strings.ContainsAny(target, " \\") {
		target = "https://" + target
	}
	return target, focus, mode
}
