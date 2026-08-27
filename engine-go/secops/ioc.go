// Package secops is Kibborg's DEFENSIVE security toolkit: deterministic log analysis, IOC
// (indicator-of-compromise) extraction, threat-pattern detection and file auditing (hash +
// entropy). Like the trading engine, every number/finding here is COMPUTED from the input —
// the LLM only interprets these facts, it never invents indicators. Strictly read-only and
// defensive: it detects and explains, it never attacks or exploits.
package secops

import (
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
)

// IOC is one extracted indicator of compromise.
type IOC struct {
	Type       string `json:"type"` // ipv4 | ipv6 | domain | url | email | md5 | sha1 | sha256
	Value      string `json:"value"`
	Count      int    `json:"count"`
	Note       string `json:"note,omitempty"` // classification (private/public/loopback/metadata…)
	Suspicious bool   `json:"suspicious"`
}

// ThreatMatch is a detected attack/abuse pattern.
type ThreatMatch struct {
	Category string `json:"category"` // sqli | xss | path_traversal | cmd_injection | secret_leak | ...
	Severity string `json:"severity"` // low | medium | high
	Count    int    `json:"count"`
	Sample   string `json:"sample"`
}

// ScanReport is the result of scanning a blob of text/logs.
type ScanReport struct {
	Bytes   int           `json:"bytes"`
	Lines   int           `json:"lines"`
	IOCs    []IOC         `json:"iocs"`
	Threats []ThreatMatch `json:"threats"`
}

var (
	reIPv4   = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	reIPv6   = regexp.MustCompile(`\b(?:[0-9A-Fa-f]{1,4}:){2,7}[0-9A-Fa-f]{0,4}\b`)
	reURL    = regexp.MustCompile(`\b(?:https?|ftp)://[^\s"'<>)\]]+`)
	reEmail  = regexp.MustCompile(`\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`)
	reDomain = regexp.MustCompile(`\b(?:[a-zA-Z0-9](?:[a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,}\b`)
	reSHA256 = regexp.MustCompile(`\b[A-Fa-f0-9]{64}\b`)
	reSHA1   = regexp.MustCompile(`\b[A-Fa-f0-9]{40}\b`)
	reMD5    = regexp.MustCompile(`\b[A-Fa-f0-9]{32}\b`)
)

// threatPattern is one attack/secret signature.
type threatPattern struct {
	category string
	severity string
	re       *regexp.Regexp
}

// threatPatterns are conservative, high-signal signatures. Case-insensitive where it matters.
var threatPatterns = []threatPattern{
	{"sqli", "high", regexp.MustCompile(`(?i)(union\s+select|or\s+1\s*=\s*1|'\s*or\s*'1'\s*=\s*'1|select\s+.*\s+from\s+information_schema|;\s*drop\s+table)`)},
	{"xss", "high", regexp.MustCompile(`(?i)(<script\b|onerror\s*=|onload\s*=|javascript:\s*[a-z]|<img[^>]+src\s*=\s*["']?\s*x\b)`)},
	{"path_traversal", "high", regexp.MustCompile(`(?:\.\./){2,}|(?:\.\.\\){2,}|(?i)/etc/passwd|(?i)\\windows\\system32\\config`)},
	{"cmd_injection", "high", regexp.MustCompile(`(?i)(;\s*rm\s+-rf|\|\s*nc\s|\|\s*bash\b|\|\s*sh\b|curl\s+[^\s]+\s*\|\s*(sh|bash)|wget\s+[^\s]+\s*\|\s*(sh|bash)|\$\(.*\)|\bpowershell\b.*-enc)`)},
	{"secret_leak", "high", regexp.MustCompile(`(?i)(-----BEGIN [A-Z ]*PRIVATE KEY-----|AKIA[0-9A-Z]{16}|\bbot\d{6,}:[A-Za-z0-9_\-]{30,}|\b\d{8,10}:[A-Za-z0-9_\-]{35}\b|(password|passwd|api[_-]?key|secret|token)\s*[=:]\s*\S{6,})`)},
	{"brute_force", "medium", regexp.MustCompile(`(?i)(failed (login|password|authentication)|authentication failure|invalid user|access denied|unauthorized|доступ (ограничен|запрещ)|401 unauthorized|403 forbidden)`)},
	{"scanner", "medium", regexp.MustCompile(`(?i)(sqlmap|nikto|nmap|masscan|dirbuster|gobuster|hydra|metasploit|/\.env\b|/wp-login\.php|/phpmyadmin)`)},
	{"reverse_shell", "high", regexp.MustCompile(`(?i)(/dev/tcp/\d|bash\s+-i\s|nc\s+-e\s|python\s+-c\s+['"]?import\s+socket|mkfifo\s+/tmp/)`)},
}

// ScanText extracts IOCs and detects threat patterns in text. Deterministic and read-only.
func ScanText(text string) ScanReport {
	rep := ScanReport{Bytes: len(text), Lines: strings.Count(text, "\n") + 1}

	// URLs first so their host IPs/domains don't double-count as bare IOCs would be noise;
	// we still extract bare ones separately (a log line can have both).
	add := map[string]*IOC{} // key = type|value
	addIOC := func(typ, val string) {
		val = strings.TrimRight(val, ".,;:)]}\"'")
		if val == "" {
			return
		}
		key := typ + "|" + val
		if e, ok := add[key]; ok {
			e.Count++
			return
		}
		ioc := &IOC{Type: typ, Value: val, Count: 1}
		classify(ioc)
		add[key] = ioc
	}

	for _, m := range reURL.FindAllString(text, -1) {
		addIOC("url", m)
	}
	for _, m := range reEmail.FindAllString(text, -1) {
		addIOC("email", m)
	}
	for _, m := range reSHA256.FindAllString(text, -1) {
		addIOC("sha256", m)
	}
	// Remove sha256 hits before matching sha1/md5 (a 64-hex contains 40/32-hex substrings).
	noSHA256 := reSHA256.ReplaceAllString(text, " ")
	for _, m := range reSHA1.FindAllString(noSHA256, -1) {
		addIOC("sha1", m)
	}
	noHashes := reMD5.ReplaceAllString(reSHA1.ReplaceAllString(noSHA256, " "), " ")
	for _, m := range reMD5.FindAllString(noHashes, -1) {
		addIOC("md5", m)
	}
	for _, m := range reIPv4.FindAllString(text, -1) {
		if validIPv4(m) {
			addIOC("ipv4", m)
		}
	}
	for _, m := range reIPv6.FindAllString(text, -1) {
		if ip := net.ParseIP(m); ip != nil && ip.To4() == nil {
			addIOC("ipv6", m)
		}
	}
	// Domains: skip ones already captured inside a URL/email to cut noise; keep standalone ones.
	seenInURLOrEmail := text
	for _, m := range reDomain.FindAllString(text, -1) {
		if looksLikeFile(m) {
			continue
		}
		// crude: only add domains that appear outside url()/email context often enough
		if strings.Contains(seenInURLOrEmail, "://"+m) || strings.Contains(seenInURLOrEmail, "@"+m) {
			continue
		}
		addIOC("domain", m)
	}

	for _, ioc := range add {
		rep.IOCs = append(rep.IOCs, *ioc)
	}
	sort.Slice(rep.IOCs, func(i, j int) bool {
		if rep.IOCs[i].Suspicious != rep.IOCs[j].Suspicious {
			return rep.IOCs[i].Suspicious
		}
		if rep.IOCs[i].Count != rep.IOCs[j].Count {
			return rep.IOCs[i].Count > rep.IOCs[j].Count
		}
		return rep.IOCs[i].Value < rep.IOCs[j].Value
	})

	// Threat patterns.
	for _, p := range threatPatterns {
		matches := p.re.FindAllString(text, -1)
		if len(matches) == 0 {
			continue
		}
		rep.Threats = append(rep.Threats, ThreatMatch{
			Category: p.category, Severity: p.severity, Count: len(matches),
			Sample: redactThreatSample(p.category, capSample(matches[0], 120)),
		})
	}
	sort.Slice(rep.Threats, func(i, j int) bool {
		if sevRank(rep.Threats[i].Severity) != sevRank(rep.Threats[j].Severity) {
			return sevRank(rep.Threats[i].Severity) > sevRank(rep.Threats[j].Severity)
		}
		return rep.Threats[i].Count > rep.Threats[j].Count
	})
	return rep
}

// classify annotates an IOC (mainly IP scope) and flags suspicious ones.
func classify(ioc *IOC) {
	switch ioc.Type {
	case "ipv4", "ipv6":
		ip := net.ParseIP(ioc.Value)
		if ip == nil {
			return
		}
		switch {
		case ip.IsLoopback():
			ioc.Note = "loopback"
		case ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast():
			ioc.Note = "link-local"
			if ioc.Value == "169.254.169.254" {
				ioc.Note = "cloud-metadata (169.254.169.254)"
				ioc.Suspicious = true
			}
		case ip.IsPrivate():
			ioc.Note = "private"
		case ip.IsUnspecified():
			ioc.Note = "unspecified"
		default:
			ioc.Note = "public"
		}
	case "url":
		low := strings.ToLower(ioc.Value)
		if strings.Contains(low, "169.254.169.254") || strings.HasPrefix(low, "file://") {
			ioc.Suspicious = true
			ioc.Note = "внутренний/метаданные"
		}
	}
}

func validIPv4(s string) bool {
	ip := net.ParseIP(s)
	return ip != nil && ip.To4() != nil
}

// looksLikeFile filters domain false-positives like "index.html" or "engine.log".
func looksLikeFile(s string) bool {
	fileExts := []string{
		".go", ".js", ".html", ".htm", ".log", ".json", ".css", ".py", ".exe", ".dll",
		".md", ".txt", ".ini", ".db", ".sqlite", ".yaml", ".yml", ".toml", ".sh", ".bat",
		".cmd", ".conf", ".cfg", ".gguf", ".bin", ".wal", ".shm", ".tmp", ".xml", ".csv",
	}
	for _, e := range fileExts {
		if strings.HasSuffix(s, e) {
			return true
		}
	}
	return false
}

func capSample(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// redactThreatSample strips secret material from report samples so Telegram/Web/MD
// do not re-publish API keys, bot tokens, or private key bodies.
func redactThreatSample(category, sample string) string {
	if category != "secret_leak" {
		return sample
	}
	low := strings.ToLower(sample)
	switch {
	case strings.Contains(low, "begin") && strings.Contains(low, "private key"):
		return "[private-key redacted]"
	case strings.Contains(sample, "AKIA"):
		return "AKIA…[aws-key redacted]"
	case strings.Contains(sample, "bot") && strings.Contains(sample, ":"):
		return "[bot-token redacted]"
	}
	// password=… / api_key=… / token: … — keep the key name, drop the value.
	for _, sep := range []string{"=", ":"} {
		if i := strings.Index(sample, sep); i > 0 {
			key := strings.TrimSpace(sample[:i])
			if len(key) > 40 {
				key = key[:40]
			}
			return key + sep + "[redacted]"
		}
	}
	return "[secret redacted]"
}

func sevRank(s string) int {
	switch s {
	case "high":
		return 3
	case "medium":
		return 2
	default:
		return 1
	}
}

// RenderMarkdown renders a scan report as a Telegram/Markdown message.
func (r ScanReport) RenderMarkdown(title string) string {
	var b strings.Builder
	if title == "" {
		title = "Скан безопасности"
	}
	fmt.Fprintf(&b, "🛡 **%s**\n", title)
	fmt.Fprintf(&b, "Объём: %d симв. · %d строк\n", r.Bytes, r.Lines)

	if len(r.Threats) > 0 {
		b.WriteString("\n⚠️ **Обнаруженные угрозы**:\n")
		for _, t := range r.Threats {
			fmt.Fprintf(&b, "- %s `%s` ×%d — %s\n", sevIcon(t.Severity), t.Category, t.Count, t.Sample)
		}
	} else {
		b.WriteString("\n✅ Явных сигнатур атак не найдено.\n")
	}

	if len(r.IOCs) > 0 {
		susp := 0
		for _, i := range r.IOCs {
			if i.Suspicious {
				susp++
			}
		}
		fmt.Fprintf(&b, "\n🔎 **IOC** (%d уник., подозрительных: %d):\n", len(r.IOCs), susp)
		shown := 0
		for _, i := range r.IOCs {
			if shown >= 20 {
				fmt.Fprintf(&b, "- …ещё %d\n", len(r.IOCs)-shown)
				break
			}
			mark := ""
			if i.Suspicious {
				mark = " 🔴"
			}
			note := ""
			if i.Note != "" {
				note = " (" + i.Note + ")"
			}
			fmt.Fprintf(&b, "- `%s` %s ×%d%s%s\n", i.Value, i.Type, i.Count, note, mark)
			shown++
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func sevIcon(sev string) string {
	switch sev {
	case "high":
		return "🔴"
	case "medium":
		return "🟡"
	default:
		return "🟢"
	}
}
