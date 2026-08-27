package secops

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ProbeFinding is one deterministic observation from an HTTP strength test.
type ProbeFinding struct {
	ID       string `json:"id"`
	Severity string `json:"severity"` // critical|high|medium|low|info
	Title    string `json:"title"`
	Where    string `json:"where"`
	Detail   string `json:"detail"`
	Fix      string `json:"fix"`
}

// URLProbe is the result of ProbeURL.
type URLProbe struct {
	URL           string            `json:"url"`
	FinalURL      string            `json:"final_url"`
	Status        int               `json:"status"`
	HTTPS         bool              `json:"https"`
	Redirects     []string          `json:"redirects,omitempty"`
	Server        string            `json:"server,omitempty"`
	PoweredBy     string            `json:"powered_by,omitempty"`
	Headers       map[string]string `json:"headers,omitempty"`
	Cookies       []string          `json:"cookies,omitempty"`
	TLSVersion    string            `json:"tls_version,omitempty"`
	SensitiveHits []string          `json:"sensitive_hits,omitempty"`
	Findings      []ProbeFinding    `json:"findings"`
	CheckedAt     string            `json:"checked_at"`
}

var sensitivePaths = []string{
	"/.env",
	"/.git/config",
	"/.git/HEAD",
	"/backup.zip",
	"/db.sql",
	"/server-status",
	"/phpinfo.php",
	"/admin",
	"/administrator",
	"/wp-login.php",
	"/robots.txt",
	"/sitemap.xml",
	"/security.txt",
	"/.well-known/security.txt",
}

// ProbeURL performs a defensive HTTP strength check against a public http(s) URL.
// It never sends exploit payloads — only GET/HEAD and header inspection.
func ProbeURL(raw string) (URLProbe, error) {
	normalized, err := safePublicURL(raw)
	if err != nil {
		return URLProbe{}, err
	}
	u, _ := url.Parse(normalized)
	rep := URLProbe{
		URL:       normalized,
		HTTPS:     u.Scheme == "https",
		Headers:   map[string]string{},
		CheckedAt: time.Now().Format(time.RFC3339),
	}

	client := &http.Client{
		Timeout: 20 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 8 {
				return fmt.Errorf("слишком много редиректов")
			}
			if hostIsInternal(req.URL.Hostname()) {
				return fmt.Errorf("редирект на внутренний хост заблокирован")
			}
			rep.Redirects = append(rep.Redirects, req.URL.String())
			return nil
		},
		Transport: &http.Transport{
			Proxy:               nil, // audits must not leak through an ambient proxy
			TLSHandshakeTimeout: 10 * time.Second,
			TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
			DialContext:         dialPublicOnly,
		},
	}

	req, err := http.NewRequest(http.MethodGet, normalized, nil)
	if err != nil {
		return rep, err
	}
	req.Header.Set("User-Agent", "Kibborg-SecurityProbe/1.0 (+authorized-audit)")
	req.Header.Set("Accept", "text/html,application/json;q=0.9,*/*;q=0.8")

	resp, err := client.Do(req)
	if err != nil {
		return rep, fmt.Errorf("запрос не удался: %w", err)
	}
	defer resp.Body.Close()
	homeBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	homeFP := makeResponseFingerprint(resp, homeBody)

	rep.Status = resp.StatusCode
	rep.FinalURL = resp.Request.URL.String()
	rep.HTTPS = resp.Request.URL.Scheme == "https"
	rep.Server = resp.Header.Get("Server")
	rep.PoweredBy = resp.Header.Get("X-Powered-By")
	interesting := []string{
		"Content-Security-Policy", "Strict-Transport-Security", "X-Frame-Options",
		"X-Content-Type-Options", "Referrer-Policy", "Permissions-Policy",
		"Cross-Origin-Opener-Policy", "Cross-Origin-Resource-Policy",
		"Access-Control-Allow-Origin", "WWW-Authenticate",
	}
	for _, h := range interesting {
		if v := resp.Header.Get(h); v != "" {
			rep.Headers[h] = v
		}
	}
	for _, c := range resp.Cookies() {
		flags := []string{c.Name}
		if c.Secure {
			flags = append(flags, "Secure")
		}
		if c.HttpOnly {
			flags = append(flags, "HttpOnly")
		}
		if c.SameSite != http.SameSiteDefaultMode {
			flags = append(flags, "SameSite="+sameSiteName(c.SameSite))
		}
		rep.Cookies = append(rep.Cookies, strings.Join(flags, "; "))
	}
	if resp.TLS != nil {
		rep.TLSVersion = tlsVersionName(resp.TLS.Version)
	}

	rep.Findings = append(rep.Findings, analyzeProbe(rep, resp)...)
	rep.SensitiveHits, rep.Findings = appendSensitivePathChecks(client, resp.Request.URL, homeFP, rep.Findings)
	return rep, nil
}

// responseFingerprint catches SPA soft-404s: Nuxt/Vue often return HTTP 200 + the same
// index.html for /.env, /.git, /backup.zip — that is NOT an exposure.
type responseFingerprint struct {
	Status int
	Type   string
	Len    string
	Prefix string
}

func makeResponseFingerprint(resp *http.Response, body []byte) responseFingerprint {
	fp := responseFingerprint{
		Status: resp.StatusCode,
		Type:   strings.ToLower(resp.Header.Get("Content-Type")),
		Len:    resp.Header.Get("Content-Length"),
	}
	n := len(body)
	if n > 120 {
		n = 120
	}
	fp.Prefix = string(body[:n])
	return fp
}

func sameSPAShell(home, other responseFingerprint) bool {
	if home.Status != other.Status {
		return false
	}
	homeHTML := strings.Contains(home.Type, "text/html")
	otherHTML := strings.Contains(other.Type, "text/html")
	if !(homeHTML && otherHTML) {
		return false
	}
	if home.Len != "" && other.Len != "" && home.Len == other.Len {
		return true
	}
	return home.Prefix != "" && home.Prefix == other.Prefix
}

func analyzeProbe(rep URLProbe, resp *http.Response) []ProbeFinding {
	var out []ProbeFinding
	final, _ := url.Parse(rep.FinalURL)

	if final != nil && final.Scheme == "http" {
		out = append(out, ProbeFinding{
			ID: "no-https", Severity: "high", Title: "Сайт отдаётся по HTTP без TLS",
			Where: rep.FinalURL, Detail: "Финальный URL использует http://",
			Fix: "Включить HTTPS (Let's Encrypt / CDN) и редирект 80→443",
		})
	}
	if final != nil && final.Scheme == "https" && resp.Header.Get("Strict-Transport-Security") == "" {
		out = append(out, ProbeFinding{
			ID: "missing-hsts", Severity: "medium", Title: "Нет заголовка HSTS",
			Where: rep.FinalURL, Detail: "Strict-Transport-Security отсутствует",
			Fix: "Добавить Strict-Transport-Security: max-age=31536000; includeSubDomains",
		})
	}
	if resp.Header.Get("Content-Security-Policy") == "" {
		out = append(out, ProbeFinding{
			ID: "missing-csp", Severity: "medium", Title: "Нет Content-Security-Policy",
			Where: rep.FinalURL, Detail: "CSP не задан — выше риск XSS-impact",
			Fix: "Задать CSP (хотя бы default-src 'self') и ужесточать постепенно",
		})
	}
	if resp.Header.Get("X-Content-Type-Options") == "" {
		out = append(out, ProbeFinding{
			ID: "missing-xcto", Severity: "low", Title: "Нет X-Content-Type-Options",
			Where: rep.FinalURL, Detail: "Браузер может MIME-sniff'ить ответы",
			Fix: "X-Content-Type-Options: nosniff",
		})
	}
	xfo := resp.Header.Get("X-Frame-Options")
	csp := resp.Header.Get("Content-Security-Policy")
	if xfo == "" && !strings.Contains(strings.ToLower(csp), "frame-ancestors") {
		out = append(out, ProbeFinding{
			ID: "clickjacking", Severity: "medium", Title: "Нет защиты от встраивания в iframe",
			Where: rep.FinalURL, Detail: "Нет X-Frame-Options и frame-ancestors в CSP",
			Fix: "X-Frame-Options: DENY (или CSP frame-ancestors 'none')",
		})
	}
	if resp.Header.Get("Referrer-Policy") == "" {
		out = append(out, ProbeFinding{
			ID: "missing-referrer", Severity: "low", Title: "Нет Referrer-Policy",
			Where: rep.FinalURL, Detail: "Referrer может утекать на сторонние сайты",
			Fix: "Referrer-Policy: strict-origin-when-cross-origin",
		})
	}
	if rep.Server != "" || rep.PoweredBy != "" {
		out = append(out, ProbeFinding{
			ID: "tech-disclosure", Severity: "info", Title: "Раскрытие технологий в заголовках",
			Where:  rep.FinalURL,
			Detail: strings.TrimSpace("Server=" + rep.Server + " X-Powered-By=" + rep.PoweredBy),
			Fix:    "Убрать или обобщить Server / X-Powered-By",
		})
	}
	acao := resp.Header.Get("Access-Control-Allow-Origin")
	if acao == "*" {
		out = append(out, ProbeFinding{
			ID: "cors-star", Severity: "medium", Title: "CORS разрешает любой Origin (*)",
			Where: rep.FinalURL, Detail: "Access-Control-Allow-Origin: *",
			Fix: "Отдавать конкретный Origin и не сочетать * с credentials",
		})
	}
	for _, c := range resp.Cookies() {
		if !c.HttpOnly {
			out = append(out, ProbeFinding{
				ID: "cookie-httponly", Severity: "medium", Title: "Cookie без HttpOnly: " + c.Name,
				Where: rep.FinalURL, Detail: "Доступна document.cookie при XSS",
				Fix: "Выставить HttpOnly на сессионные cookie",
			})
		}
		if final != nil && final.Scheme == "https" && !c.Secure {
			out = append(out, ProbeFinding{
				ID: "cookie-secure", Severity: "medium", Title: "Cookie без Secure: " + c.Name,
				Where: rep.FinalURL, Detail: "Может уйти по чистому HTTP",
				Fix: "Выставить Secure на cookie",
			})
		}
		if c.SameSite == http.SameSiteDefaultMode {
			out = append(out, ProbeFinding{
				ID: "cookie-samesite", Severity: "low", Title: "Cookie без SameSite: " + c.Name,
				Where: rep.FinalURL, Detail: "SameSite не задан явно",
				Fix: "SameSite=Lax или Strict для сессий",
			})
		}
	}
	if len(out) == 0 {
		out = append(out, ProbeFinding{
			ID: "baseline-ok", Severity: "info", Title: "Базовые заголовки выглядят приемлемо",
			Where: rep.FinalURL, Detail: "Критичных пропусков на уровне HTTP-пробы не видно",
			Fix: "Продолжить ручной/инструментальный разбор приложения",
		})
	}
	return out
}

func appendSensitivePathChecks(client *http.Client, base *url.URL, home responseFingerprint, findings []ProbeFinding) ([]string, []ProbeFinding) {
	if base == nil {
		return nil, findings
	}
	var hits []string
	spaSoft404 := 0
	for _, p := range sensitivePaths {
		u := *base
		u.Path = p
		u.RawQuery = ""
		u.Fragment = ""
		req, err := http.NewRequest(http.MethodGet, u.String(), nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", "Kibborg-SecurityProbe/1.0 (+authorized-audit)")
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		fp := makeResponseFingerprint(resp, body)
		// robots/sitemap/security.txt are expected; only flag "interesting" exposures.
		interesting := p != "/robots.txt" && p != "/sitemap.xml" &&
			p != "/security.txt" && p != "/.well-known/security.txt"
		if resp.StatusCode < 200 || resp.StatusCode >= 300 || !interesting {
			continue
		}
		if sameSPAShell(home, fp) {
			spaSoft404++
			continue // SPA catch-all HTML, not a real .env/.git leak
		}
		hits = append(hits, fmt.Sprintf("%s → %d", p, resp.StatusCode))
		sev := "high"
		if p == "/admin" || p == "/administrator" || p == "/wp-login.php" {
			sev = "medium"
		}
		findings = append(findings, ProbeFinding{
			ID: "exposed-path", Severity: sev,
			Title:  "Доступен чувствительный путь: " + p,
			Where:  u.String(),
			Detail: fmt.Sprintf("HTTP %d на %s (тело не похоже на оболочку главной)", resp.StatusCode, p),
			Fix:    "Закрыть путь на уровне сервера/WAF или убрать артефакт с продакшена",
		})
	}
	if spaSoft404 >= 3 {
		findings = append(findings, ProbeFinding{
			ID: "spa-soft-404", Severity: "info",
			Title:  "SPA отдаёт HTTP 200 на несуществующие пути",
			Where:  base.String(),
			Detail: fmt.Sprintf("%d чувствительных URL вернули ту же HTML-оболочку, что и главная (типичный catch-all Nuxt/Vue)", spaSoft404),
			Fix:    "На nginx/CDN отдавать 404 для статических секретных путей (/.env, /.git, *.sql, *.zip) до SPA fallback",
		})
	}
	return hits, findings
}

// RenderMarkdown formats the probe as a human-readable report block.
func (p URLProbe) RenderMarkdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "🛡 **HTTP-проба** `%s`\n", p.URL)
	fmt.Fprintf(&b, "- финальный URL: `%s`\n", p.FinalURL)
	fmt.Fprintf(&b, "- статус: %d · HTTPS: %v · TLS: %s\n", p.Status, p.HTTPS, dash(p.TLSVersion))
	if p.Server != "" || p.PoweredBy != "" {
		fmt.Fprintf(&b, "- Server: `%s` · X-Powered-By: `%s`\n", dash(p.Server), dash(p.PoweredBy))
	}
	if len(p.Redirects) > 0 {
		fmt.Fprintf(&b, "- редиректы: %s\n", strings.Join(p.Redirects, " → "))
	}
	if len(p.Headers) > 0 {
		b.WriteString("- security-заголовки:\n")
		for k, v := range p.Headers {
			fmt.Fprintf(&b, "  - `%s`: %s\n", k, trimOneLine(v, 120))
		}
	}
	if len(p.Cookies) > 0 {
		b.WriteString("- cookies: " + strings.Join(p.Cookies, "; ") + "\n")
	}
	if len(p.SensitiveHits) > 0 {
		b.WriteString("- чувствительные пути: " + strings.Join(p.SensitiveHits, ", ") + "\n")
	}
	b.WriteString("\n**Находки:**\n")
	for i, f := range p.Findings {
		fmt.Fprintf(&b, "%d. [%s] **%s**\n", i+1, strings.ToUpper(f.Severity), f.Title)
		fmt.Fprintf(&b, "   где: %s\n", f.Where)
		fmt.Fprintf(&b, "   факт: %s\n", f.Detail)
		fmt.Fprintf(&b, "   лечение: %s\n", f.Fix)
	}
	return b.String()
}

func safePublicURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("некорректный URL: %w", err)
	}
	if u.Scheme == "" {
		u.Scheme = "https"
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("разрешены только http/https")
	}
	host := u.Hostname()
	if host == "" {
		return "", fmt.Errorf("URL без хоста")
	}
	if hostIsInternal(host) {
		return "", fmt.Errorf("доступ к внутренним/приватным адресам запрещён: %s", host)
	}
	return u.String(), nil
}

func hostIsInternal(host string) bool {
	h := strings.ToLower(strings.Trim(host, "[]"))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") ||
		strings.HasSuffix(h, ".local") || strings.HasSuffix(h, ".internal") {
		return true
	}
	if looksNonCanonicalIP(h) {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ipIsInternal(ip)
	}
	// Lookup errors fail OPEN at check time; dialPublicOnly fails closed at connect.
	ips, err := net.LookupIP(host)
	if err != nil {
		return false
	}
	for _, ip := range ips {
		if ipIsInternal(ip) {
			return true
		}
	}
	return false
}

func looksNonCanonicalIP(host string) bool {
	h := strings.ToLower(strings.Trim(host, "[]"))
	if strings.HasPrefix(h, "0x") {
		return true
	}
	if h == "" {
		return false
	}
	for _, r := range h {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// dialPublicOnly re-resolves and refuses internal IPs at connect time (DNS-rebind TOCTOU).
func dialPublicOnly(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if looksNonCanonicalIP(host) || hostIsInternal(host) {
		return nil, fmt.Errorf("доступ к внутренним/приватным адресам запрещён: %s", host)
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return nil, fmt.Errorf("DNS lookup failed (fail-closed): %w", err)
	}
	d := &net.Dialer{Timeout: 10 * time.Second}
	var lastErr error
	for _, ip := range ips {
		if ipIsInternal(ip) {
			lastErr = fmt.Errorf("внутренний адрес заблокирован: %s", ip)
			continue
		}
		conn, derr := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if derr == nil {
			return conn, nil
		}
		lastErr = derr
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("нет публичных адресов для %s", host)
	}
	return nil, lastErr
}

func ipIsInternal(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil && ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
		return true
	}
	return false
}

func tlsVersionName(v uint16) string {
	switch v {
	case tls.VersionTLS13:
		return "TLS1.3"
	case tls.VersionTLS12:
		return "TLS1.2"
	case tls.VersionTLS11:
		return "TLS1.1"
	case tls.VersionTLS10:
		return "TLS1.0"
	default:
		return fmt.Sprintf("0x%04x", v)
	}
}

func sameSiteName(v http.SameSite) string {
	switch v {
	case http.SameSiteStrictMode:
		return "Strict"
	case http.SameSiteLaxMode:
		return "Lax"
	case http.SameSiteNoneMode:
		return "None"
	default:
		return "Default"
	}
}

func dash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func trimOneLine(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
