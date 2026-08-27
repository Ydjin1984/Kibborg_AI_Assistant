package secops

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const securityReportDir = "runtime/browser/security"

var unsafeNameChars = regexp.MustCompile(`[^a-zA-Z0-9._\-а-яА-ЯёЁ]+`)
var reportJWT = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`)
var reportPasswordLine = regexp.MustCompile(`(?i)(\*{0,2}password\*{0,2}\s*[:：]\s*` + "`" + `?)([^\s` + "`" + `\n]+)`)
var reportPasswordParen = regexp.MustCompile(`(?i)([Пп]ароль\s*\([^)]*\)\s*)([^\s\n]+)`)
var reportPasswordWord = regexp.MustCompile(`(?i)((?:password|пароль)\s*[:=]\s*)([^\s\n]+)`)

// SecurityReportInput is what write_security_report receives.
type SecurityReportInput struct {
	Title  string
	Target string
	Body   string // markdown findings; wrapped into a full document
}

// SecurityReportResult points at the written file.
type SecurityReportResult struct {
	Path string
	URL  string // relative path under runtime/browser for /api/files
}

// urlInMarkdown finds the first http(s) URL in free text (report body / title).
var urlInMarkdown = regexp.MustCompile(`https?://[^\s)\]<>"']+`)

// inferTargetFromMarkdown recovers target when the model put the URL only inside body/title.
// Live failure: write_security_report three times with a full body and empty target → task expired.
func inferTargetFromMarkdown(title, body string) string {
	for _, line := range strings.Split(body, "\n") {
		low := strings.ToLower(strings.TrimSpace(line))
		if strings.Contains(low, "цель") || strings.Contains(low, "target") {
			if m := urlInMarkdown.FindString(line); m != "" {
				return strings.TrimRight(m, ".,;:)")
			}
		}
	}
	if m := urlInMarkdown.FindString(title); m != "" {
		return strings.TrimRight(m, ".,;:)")
	}
	if m := urlInMarkdown.FindString(body); m != "" {
		return strings.TrimRight(m, ".,;:)")
	}
	return ""
}

// WriteSecurityReport writes a structured Markdown audit report under runtime/browser/security.
func WriteSecurityReport(in SecurityReportInput) (SecurityReportResult, error) {
	title := strings.TrimSpace(in.Title)
	target := strings.TrimSpace(in.Target)
	body := strings.TrimSpace(in.Body)
	if title == "" {
		title = "Отчёт теста на прочность"
	}
	if target == "" {
		target = inferTargetFromMarkdown(title, body)
	}
	if target == "" {
		return SecurityReportResult{}, fmt.Errorf("нужен target (URL или хост) — передай target=https://… или укажи URL в body")
	}
	if body == "" {
		return SecurityReportResult{}, fmt.Errorf("нужен body — находки в Markdown")
	}
	// Не кладём пароли/JWT из фокуса пентеста в MD, который уйдёт разработчикам.
	body = scrubReportSecrets(body)

	if err := os.MkdirAll(securityReportDir, 0o755); err != nil {
		return SecurityReportResult{}, err
	}

	stamp := time.Now().Format("20060102-150405")
	host := sanitizeReportName(target)
	name := fmt.Sprintf("%s-%s.md", stamp, host)
	path := filepath.Join(securityReportDir, name)

	doc := buildSecurityMarkdown(title, target, body)
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		return SecurityReportResult{}, err
	}
	rel := filepath.ToSlash(filepath.Join("security", name))
	return SecurityReportResult{Path: path, URL: rel}, nil
}

// ListSecurityReports returns newest report paths first (basename + abs path).
func ListSecurityReports(limit int) ([]string, error) {
	entries, err := os.ReadDir(securityReportDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
			continue
		}
		names = append(names, filepath.Join(securityReportDir, e.Name()))
		if limit > 0 && len(names) >= limit {
			break
		}
	}
	return names, nil
}

func buildSecurityMarkdown(title, target, body string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", title)
	fmt.Fprintf(&b, "- **Цель:** `%s`\n", target)
	fmt.Fprintf(&b, "- **Когда:** %s\n", time.Now().Format(time.RFC3339))
	fmt.Fprintf(&b, "- **Движок:** Kibborg secops (авторизованный тест на прочность)\n")
	fmt.Fprintf(&b, "- **Каталог инструментов:** `hacker-tools/` (Awesome-Hacking)\n\n")
	b.WriteString("## Scope\n\n")
	b.WriteString("Проверка выполняется только для цели, указанной владельцем. ")
	b.WriteString("Отчёт описывает находки, шаги воспроизведения проверки и рекомендации по лечению.\n\n")
	b.WriteString("## Находки\n\n")
	b.WriteString(body)
	if !strings.HasSuffix(body, "\n") {
		b.WriteByte('\n')
	}
	b.WriteString("\n## Шаблон находки\n\n")
	b.WriteString("Каждая дыра в идеале содержит: **где**, **что не так**, **как воспроизвести**, **как лечить**, **severity**.\n")
	return b.String()
}

func sanitizeReportName(target string) string {
	s := strings.TrimSpace(strings.ToLower(target))
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	s = unsafeNameChars.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-.")
	if s == "" {
		s = "target"
	}
	if len(s) > 60 {
		s = s[:60]
	}
	return s
}

// scrubReportSecrets strips credentials the model often copies from the user focus into MD.
func scrubReportSecrets(body string) string {
	body = reportJWT.ReplaceAllString(body, "eyJ***")
	body = reportPasswordWord.ReplaceAllString(body, `${1}***`)
	body = reportPasswordLine.ReplaceAllString(body, `${1}***`)
	body = reportPasswordParen.ReplaceAllString(body, `${1}***`)
	// Common «Password: `Secret`» / «Пароль: Secret» already covered; also bare SystemX-like
	// lines under a password heading are left — better than over-redacting findings.
	return body
}
