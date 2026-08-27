package secops

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	securityEvidenceDir = "runtime/browser/security/evidence"
	maxDownloadBytes    = 32 << 20 // 32 MiB — evidence dump, not a mirror
)

// DownloadResult is a file saved under runtime/browser for /api/files links.
type DownloadResult struct {
	Path        string
	URL         string // relative under runtime/browser
	Bytes       int
	ContentType string
	FinalURL    string
	Preview     string // short text preview when body is text
	Warning     string // non-fatal note (e.g. unusual type)
}

// DownloadURL fetches a public URL and saves it under runtime/browser/security/evidence.
// SPA catch-all HTML for secret-looking paths is rejected (not evidence of a leak).
func DownloadURL(rawURL, filename string) (DownloadResult, error) {
	safe, err := safePublicURL(rawURL)
	if err != nil {
		return DownloadResult{}, err
	}
	client := &http.Client{
		Timeout: 45 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 8 {
				return fmt.Errorf("слишком много редиректов")
			}
			if hostIsInternal(req.URL.Hostname()) {
				return fmt.Errorf("редирект на внутренний хост заблокирован")
			}
			return nil
		},
		Transport: &http.Transport{
			Proxy:               nil,
			TLSHandshakeTimeout: 10 * time.Second,
			DialContext:         dialPublicOnly,
		},
	}
	req, err := http.NewRequest(http.MethodGet, safe, nil)
	if err != nil {
		return DownloadResult{}, err
	}
	req.Header.Set("User-Agent", "Kibborg-SecurityProbe/1.0 (+authorized-audit)")
	req.Header.Set("Accept", "*/*")

	resp, err := client.Do(req)
	if err != nil {
		return DownloadResult{}, fmt.Errorf("запрос не удался: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return DownloadResult{}, fmt.Errorf("HTTP %d для %s", resp.StatusCode, safe)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDownloadBytes+1))
	if err != nil {
		return DownloadResult{}, err
	}
	if len(body) > maxDownloadBytes {
		return DownloadResult{}, fmt.Errorf("ответ больше %d МБ — не сохраняю", maxDownloadBytes>>20)
	}

	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	finalURL := safe
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	htmlBody := looksLikeHTMLBody(ct, body)
	// Live: /admin,/wp-login,/server-status отдавали одну и ту же Nuxt-оболочку 7142 байт —
	// агент клал их в evidence как «доказательства». Это soft-404 SPA, не утечка.
	if htmlBody && (looksLikeSecretPath(safe) || looksLikeProbeTrapPath(safe) || looksLikeSPAAppShell(body)) {
		if !isBenignHTMLPath(safe) {
			return DownloadResult{}, fmt.Errorf(
				"ответ — HTML-оболочка SPA (не содержимое файла/API) для %s. Это soft-404 catch-all, не утечка. Не сохраняю",
				safe)
		}
	}

	name := strings.TrimSpace(filename)
	if name == "" {
		name = suggestDownloadName(safe, ct, body)
	}
	name = sanitizeEvidenceName(name)
	if err := os.MkdirAll(securityEvidenceDir, 0o755); err != nil {
		return DownloadResult{}, err
	}
	stamp := time.Now().Format("20060102-150405")
	fullName := stamp + "-" + name
	pathOnDisk := filepath.Join(securityEvidenceDir, fullName)
	if err := os.WriteFile(pathOnDisk, body, 0o644); err != nil {
		return DownloadResult{}, err
	}
	rel := filepath.ToSlash(filepath.Join("security", "evidence", fullName))
	out := DownloadResult{
		Path:        pathOnDisk,
		URL:         rel,
		Bytes:       len(body),
		ContentType: ct,
		FinalURL:    finalURL,
		Preview:     textPreview(body, ct),
	}
	if htmlBody {
		out.Warning = "тело похоже на HTML — проверь, что это нужный артефакт, а не оболочка SPA"
	}
	return out, nil
}

func looksLikeSecretPath(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	p := strings.ToLower(u.Path)
	secrets := []string{
		"/.env", "/.git/", "/backup", ".sql", "phpinfo", "wp-config",
		"/id_rsa", "/.aws/", "/credentials", "/config.json", "/dump",
	}
	for _, s := range secrets {
		if strings.Contains(p, s) {
			return true
		}
	}
	return false
}

// looksLikeProbeTrapPath — классические пути «утечки», которые на SPA всегда 200+HTML.
func looksLikeProbeTrapPath(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	p := strings.ToLower(strings.TrimSuffix(u.Path, "/"))
	traps := []string{
		"/admin", "/administrator", "/wp-login.php", "/wp-login", "/wp-admin",
		"/server-status", "/server-info", "/.well-known/security.txt",
		"/phpmyadmin", "/pma", "/manager/html",
	}
	for _, t := range traps {
		if p == t || strings.HasSuffix(p, t) {
			return true
		}
	}
	return false
}

func looksLikeSPAAppShell(body []byte) bool {
	sample := strings.ToLower(string(body))
	if len(sample) > 4000 {
		sample = sample[:4000]
	}
	markers := []string{
		"nuxt-color-mode", "__nuxt__", "id=\"__nuxt\"", "data-v-app",
		"vite/client", "/_nuxt/", "id=\"__next\"", "__NEXT_DATA__",
		"ng-version", "data-reactroot",
	}
	hits := 0
	for _, m := range markers {
		if strings.Contains(sample, m) {
			hits++
		}
	}
	return hits >= 1 && strings.Contains(sample, "<!doctype html")
}

func isBenignHTMLPath(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	p := strings.TrimSuffix(u.Path, "/")
	return p == "" || p == "/" || strings.HasSuffix(strings.ToLower(p), ".html") ||
		strings.HasSuffix(strings.ToLower(p), ".htm")
}

func looksLikeHTMLBody(ct string, body []byte) bool {
	if strings.Contains(ct, "text/html") {
		return true
	}
	sample := strings.ToLower(string(body))
	if len(sample) > 800 {
		sample = sample[:800]
	}
	return strings.Contains(sample, "<!doctype html") ||
		strings.Contains(sample, "<html") ||
		(strings.Contains(sample, "<head") && strings.Contains(sample, "<body"))
}

func suggestDownloadName(rawURL, ct string, body []byte) string {
	u, err := url.Parse(rawURL)
	base := ""
	if err == nil {
		base = path.Base(u.Path)
	}
	if base == "" || base == "/" || base == "." {
		base = "download"
	}
	if strings.Contains(base, "?") {
		base = strings.Split(base, "?")[0]
	}
	if filepath.Ext(base) == "" {
		switch {
		case strings.Contains(ct, "json"):
			base += ".json"
		case strings.Contains(ct, "zip") || looksZip(body):
			base += ".zip"
		case strings.Contains(ct, "sql") || looksSQL(body):
			base += ".sql"
		case strings.Contains(ct, "text/plain"):
			base += ".txt"
		case strings.Contains(ct, "markdown"):
			base += ".md"
		case looksLikeHTMLBody(ct, body):
			base += ".html"
		}
	}
	return base
}

func sanitizeEvidenceName(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	name = unsafeNameChars.ReplaceAllString(name, "_")
	name = strings.Trim(name, "._")
	if name == "" {
		name = "download.bin"
	}
	if len(name) > 80 {
		ext := filepath.Ext(name)
		stem := strings.TrimSuffix(name, ext)
		if len(ext) > 20 {
			ext = ""
		}
		if len(stem) > 80-len(ext) {
			stem = stem[:80-len(ext)]
		}
		name = stem + ext
	}
	return name
}

func looksZip(b []byte) bool {
	return len(b) >= 4 && b[0] == 'P' && b[1] == 'K'
}

func looksSQL(b []byte) bool {
	s := strings.ToLower(string(b))
	if len(s) > 400 {
		s = s[:400]
	}
	return strings.Contains(s, "create table") || strings.Contains(s, "insert into") ||
		strings.HasPrefix(strings.TrimSpace(s), "--") && strings.Contains(s, "mysql")
}

func textPreview(body []byte, ct string) string {
	if looksZip(body) || (!utf8.Valid(body) && !strings.Contains(ct, "text") && !strings.Contains(ct, "json")) {
		return ""
	}
	s := string(body)
	if !utf8.ValidString(s) {
		return ""
	}
	s = strings.TrimSpace(s)
	if len(s) > 240 {
		s = s[:240] + "…"
	}
	return s
}
