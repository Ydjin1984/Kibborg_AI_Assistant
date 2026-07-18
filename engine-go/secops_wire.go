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
	logsCommands  = []string{"/logs", "/логи", "/log_analyze"}
	scanCommands  = []string{"/scan", "/скан", "/ioc"}
	auditCommands = []string{"/audit", "/аудит", "/hash"}
)

// secSystemPrompt turns the model into a grounded SOC analyst: it interprets the deterministic
// findings, it does not invent them.
const secSystemPrompt = `Ты — старший аналитик по кибербезопасности (SOC / incident response) внутри Kibborg. Тебе дают ГОТОВЫЙ детерминированный разбор (уровни логов, аномалии, IOC, сигнатуры угроз, аудит файла), посчитанный движком.

Правила:
- Опирайся ТОЛЬКО на предоставленные факты. Не выдумывай IOC, адреса, хеши или выводы, которых нет в данных.
- Дай краткую оценку: что происходит, насколько серьёзно (🟢/🟡/🔴), и что делать — приоритизированные ОБОРОНИТЕЛЬНЫЕ шаги (реагирование, харденинг, чистка, ротация ключей).
- Только защита. Никаких инструкций по атаке/эксплуатации.
- По-русски, компактно, по делу. Оформляй СПИСКАМИ, без markdown-таблиц (они не отображаются в Telegram). Если данных недостаточно для вывода — так и скажи, не додумывай.`

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
				"Укажи файл: `/logs имя.log` (в каталоге движка) или пришли лог файлом с подписью `/logs`."
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
