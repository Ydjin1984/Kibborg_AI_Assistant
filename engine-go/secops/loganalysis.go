package secops

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// LogReport is the deterministic analysis of a log file.
type LogReport struct {
	Source       string         `json:"source"`
	TotalLines   int            `json:"total_lines"`
	Levels       map[string]int `json:"levels"`
	FirstLine    string         `json:"first_line"`
	LastLine     string         `json:"last_line"`
	ErrorSamples []string       `json:"error_samples"`
	TopMessages  []MsgCount     `json:"top_messages"`
	Anomalies    []Anomaly      `json:"anomalies"`
	Scan         ScanReport     `json:"scan"`
}

// MsgCount is a normalized message and how often it repeats.
type MsgCount struct {
	Message string `json:"message"`
	Count   int    `json:"count"`
}

// Anomaly is a flagged issue in the log.
type Anomaly struct {
	Severity string `json:"severity"` // low | medium | high
	Message  string `json:"message"`
}

var (
	reNum       = regexp.MustCompile(`\d+`)
	reHexBlob   = regexp.MustCompile(`\b[0-9a-fA-F]{8,}\b`)
	reLeadingTS = regexp.MustCompile(`^\s*[\[\(]?\d{2,4}[-/.:]\d{1,2}[-/.:]\d{1,4}[ T]?\d{0,2}:?\d{0,2}:?\d{0,2}[.\d]*Z?[\]\)]?\s*`)
)

// classifyLevel guesses a log line's severity from its text (works for Go's default logger,
// Kibborg's [TAG] style, syslog and JSON logs alike — none of which mark a level uniformly).
func classifyLevel(line string) string {
	l := strings.ToLower(line)
	switch {
	case strings.Contains(l, "panic") || strings.Contains(l, "fatal"):
		return "fatal"
	case strings.Contains(l, "error") || strings.Contains(l, "err ") ||
		strings.Contains(l, "ошибка") || strings.Contains(l, "❌") || strings.Contains(l, "не смог") ||
		strings.Contains(l, "failed") || strings.Contains(l, "exception"):
		return "error"
	case strings.Contains(l, "warn") || strings.Contains(l, "⚠") || strings.Contains(l, "внимание"):
		return "warn"
	case strings.Contains(l, "debug"):
		return "debug"
	default:
		return "info"
	}
}

// normalizeMsg strips timestamps, numbers and hex blobs so repeated errors that differ only in
// ids/values collapse to one signature for the top-messages tally.
func normalizeMsg(line string) string {
	s := reLeadingTS.ReplaceAllString(line, "")
	s = reHexBlob.ReplaceAllString(s, "<hex>")
	s = reNum.ReplaceAllString(s, "#")
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 160 {
		s = s[:160]
	}
	return s
}

// AnalyzeLog computes levels, top repeated messages, anomalies and an IOC/threat scan over
// the log content. `source` is a label (e.g. the file name) for the report header.
func AnalyzeLog(content, source string) LogReport {
	rep := LogReport{Source: source, Levels: map[string]int{}}
	rawLines := strings.Split(content, "\n")
	var lines []string
	for _, l := range rawLines {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	rep.TotalLines = len(lines)
	if rep.TotalLines == 0 {
		return rep
	}
	rep.FirstLine = capSample(lines[0], 200)
	rep.LastLine = capSample(lines[len(lines)-1], 200)

	msgCounts := map[string]int{}
	var errorLines []string
	maxRun, curRun := 0, 0 // longest consecutive run of error/fatal lines (burst detector)

	for _, line := range lines {
		lvl := classifyLevel(line)
		rep.Levels[lvl]++
		msgCounts[normalizeMsg(line)]++
		if lvl == "error" || lvl == "fatal" {
			curRun++
			if curRun > maxRun {
				maxRun = curRun
			}
			if len(errorLines) < 10 {
				errorLines = append(errorLines, capSample(line, 200))
			}
		} else {
			curRun = 0
		}
	}
	rep.ErrorSamples = errorLines

	// Top repeated messages.
	for msg, c := range msgCounts {
		rep.TopMessages = append(rep.TopMessages, MsgCount{Message: msg, Count: c})
	}
	sort.Slice(rep.TopMessages, func(i, j int) bool {
		if rep.TopMessages[i].Count != rep.TopMessages[j].Count {
			return rep.TopMessages[i].Count > rep.TopMessages[j].Count
		}
		return rep.TopMessages[i].Message < rep.TopMessages[j].Message
	})
	if len(rep.TopMessages) > 8 {
		rep.TopMessages = rep.TopMessages[:8]
	}

	// IOC / threat scan over the whole log.
	rep.Scan = ScanText(content)

	rep.Anomalies = detectAnomalies(rep, maxRun)
	return rep
}

func detectAnomalies(rep LogReport, maxErrorRun int) []Anomaly {
	var a []Anomaly
	errs := rep.Levels["error"] + rep.Levels["fatal"]

	if rep.Levels["fatal"] > 0 {
		a = append(a, Anomaly{"high", fmt.Sprintf("Найдены panic/fatal (%d) — процесс мог падать.", rep.Levels["fatal"])})
	}
	if rep.TotalLines > 0 {
		rate := float64(errs) / float64(rep.TotalLines)
		if rate >= 0.3 && errs >= 5 {
			a = append(a, Anomaly{"medium", fmt.Sprintf("Высокая доля ошибок: %.0f%% строк (%d из %d).", rate*100, errs, rep.TotalLines)})
		}
	}
	if maxErrorRun >= 5 {
		a = append(a, Anomaly{"medium", fmt.Sprintf("Всплеск ошибок: %d подряд — похоже на каскадный сбой.", maxErrorRun)})
	}
	// A single message repeated a lot usually means a stuck retry loop.
	if len(rep.TopMessages) > 0 {
		top := rep.TopMessages[0]
		if top.Count >= 10 && rep.TotalLines > 0 && float64(top.Count)/float64(rep.TotalLines) >= 0.25 {
			a = append(a, Anomaly{"medium", fmt.Sprintf("Одно сообщение повторяется %d раз — возможно, зациклился ретрай: «%s».", top.Count, top.Message)})
		}
	}
	// Security signals surfaced from the embedded scan.
	for _, t := range rep.Scan.Threats {
		switch t.Category {
		case "secret_leak":
			a = append(a, Anomaly{"high", fmt.Sprintf("В логе есть похожее на СЕКРЕТ (токен/ключ/пароль) ×%d — почисти лог и ротируй ключи.", t.Count)})
		case "brute_force":
			a = append(a, Anomaly{"medium", fmt.Sprintf("Повторяющиеся отказы доступа ×%d — возможен перебор/брутфорс.", t.Count)})
		case "scanner", "reverse_shell", "sqli", "xss", "path_traversal", "cmd_injection":
			a = append(a, Anomaly{t.Severity, fmt.Sprintf("Признаки атаки (%s) ×%d в логе — проверь источник.", t.Category, t.Count)})
		}
	}
	suspIP := 0
	for _, i := range rep.Scan.IOCs {
		if i.Suspicious {
			suspIP++
		}
	}
	if suspIP > 0 {
		a = append(a, Anomaly{"medium", fmt.Sprintf("Подозрительные адреса/URL в логе: %d (внутренние/метаданные).", suspIP)})
	}
	return a
}

// RenderMarkdown renders the log analysis as a Telegram/Markdown message.
func (r LogReport) RenderMarkdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "📊 **Анализ лога**: `%s`\n", r.Source)
	if r.TotalLines == 0 {
		b.WriteString("Лог пуст.")
		return b.String()
	}
	fmt.Fprintf(&b, "Строк: %d · 🔴 error: %d · 🟥 fatal: %d · 🟡 warn: %d · info: %d\n",
		r.TotalLines, r.Levels["error"], r.Levels["fatal"], r.Levels["warn"], r.Levels["info"])

	if len(r.Anomalies) > 0 {
		b.WriteString("\n⚠️ **Аномалии**:\n")
		for _, an := range r.Anomalies {
			fmt.Fprintf(&b, "- %s %s\n", sevIcon(an.Severity), an.Message)
		}
	} else {
		b.WriteString("\n✅ Явных аномалий не найдено.\n")
	}

	if len(r.TopMessages) > 0 {
		b.WriteString("\n🔁 **Частые сообщения**:\n")
		for i, m := range r.TopMessages {
			if i >= 5 {
				break
			}
			fmt.Fprintf(&b, "- ×%d — %s\n", m.Count, m.Message)
		}
	}

	if len(r.ErrorSamples) > 0 {
		b.WriteString("\n🧨 **Примеры ошибок**:\n")
		for i, e := range r.ErrorSamples {
			if i >= 3 {
				break
			}
			b.WriteString("- `" + e + "`\n")
		}
	}

	if len(r.Scan.Threats) > 0 || len(r.Scan.IOCs) > 0 {
		b.WriteString("\n")
		b.WriteString(r.Scan.RenderMarkdown("IOC/угрозы из лога"))
	}
	return strings.TrimRight(b.String(), "\n")
}
