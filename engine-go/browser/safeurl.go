package browser

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

// This file hardens the browser agent against SSRF/LFI. The agent is driven by an LLM whose
// input can include untrusted page content (indirect prompt injection), so any tool that
// fetches a URL or touches a local path must refuse internal/private targets and paths
// outside the artifact directory. Without this, open_url("file:///.../id_rsa") + get_text
// exfiltrates local files, and download_file("http://169.254.169.254/…") reaches cloud
// metadata.

// safeRemoteURL validates that rawURL is an http(s) URL pointing at a public host. It returns
// the normalized URL or an error. Dial-time re-check happens in dialPublicOnly / safeHTTPClient
// so a DNS rebind between check and connect cannot reach RFC1918 / link-local.
func safeRemoteURL(rawURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", fmt.Errorf("некорректный URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("разрешены только http/https (получено %q) — file://, chrome://, data: заблокированы", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return "", fmt.Errorf("URL без хоста")
	}
	if looksNonCanonicalIP(host) {
		return "", fmt.Errorf("неканонический IP-адрес запрещён: %s", host)
	}
	if hostIsInternal(host) {
		return "", fmt.Errorf("доступ к внутренним/приватным адресам запрещён: %s", host)
	}
	return u.String(), nil
}

// looksNonCanonicalIP catches decimal/hex forms Chrome may still treat as loopback
// (2130706433, 0x7f000001) that net.ParseIP rejects.
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
	return len(h) > 0 // pure decimal integer hostname
}

// hostIsInternal reports whether host (name or IP literal) resolves to a loopback, private,
// link-local, or metadata address that the agent must not reach.
func hostIsInternal(host string) bool {
	h := strings.ToLower(strings.Trim(host, "[]"))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") ||
		strings.HasSuffix(h, ".local") || strings.HasSuffix(h, ".internal") {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ipIsInternal(ip)
	}
	if looksNonCanonicalIP(h) {
		return true
	}
	// Block if ANY resolved address is internal. Lookup errors fail OPEN here (host may be
	// briefly unresolvable); dialPublicOnly fails closed at connect time.
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

func ipIsInternal(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true // link-local covers 169.254.169.254 (cloud metadata) and fe80::/10
	}
	if ip4 := ip.To4(); ip4 != nil && ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
		return true // 100.64.0.0/10 CGNAT
	}
	return false
}

// blockInternalRedirects rejects hops onto loopback/private hosts after the first URL.
func blockInternalRedirects(req *http.Request, via []*http.Request) error {
	if len(via) >= 8 {
		return fmt.Errorf("слишком много редиректов")
	}
	if hostIsInternal(req.URL.Hostname()) {
		return fmt.Errorf("редирект на внутренний хост заблокирован")
	}
	return nil
}

// safeHTTPClient returns an HTTP client that blocks redirects to internal hosts and dials
// only after re-checking the resolved IP (closes the DNS-rebind TOCTOU window).
// Use for reach/download of untrusted public URLs — not for loopback search engines.
func safeHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:       timeout,
		CheckRedirect: blockInternalRedirects,
		Transport: &http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			TLSHandshakeTimeout: 10 * time.Second,
			DialContext:         dialPublicOnly,
		},
	}
}

// redirectSafeClient blocks internal redirects but still dials loopback (local search
// engines / httptest). Do NOT use for untrusted URL fetches — use safeHTTPClient.
func redirectSafeClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:       timeout,
		CheckRedirect: blockInternalRedirects,
	}
}

// dialPublicOnly resolves addr, refuses any internal IP, and dials a public one.
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

// safeArtifactPath ensures a local path stays inside the artifact directory, so the agent can
// only read/write files it produced — never arbitrary local files (e.g. ~/.ssh/id_rsa via
// upload_file, or overwriting configs via a crafted clone dir).
func safeArtifactPath(p string) (string, error) {
	root, err := filepath.Abs(artifactDir)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	if abs != root && !strings.HasPrefix(abs, root+string(filepath.Separator)) {
		return "", fmt.Errorf("путь вне рабочего каталога агента (%s): %s", artifactDir, p)
	}
	return abs, nil
}
