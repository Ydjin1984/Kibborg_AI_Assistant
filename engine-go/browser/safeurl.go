package browser

import (
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strings"
)

// This file hardens the browser agent against SSRF/LFI. The agent is driven by an LLM whose
// input can include untrusted page content (indirect prompt injection), so any tool that
// fetches a URL or touches a local path must refuse internal/private targets and paths
// outside the artifact directory. Without this, open_url("file:///.../id_rsa") + get_text
// exfiltrates local files, and download_file("http://169.254.169.254/…") reaches cloud
// metadata.

// safeRemoteURL validates that rawURL is an http(s) URL pointing at a public host. It returns
// the normalized URL or an error. NOTE: this resolves DNS at check time; a determined attacker
// could still rebind between check and fetch (TOCTOU). For a local single-user tool this is an
// accepted limitation — full protection needs a custom dialer that re-checks the dialed IP.
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
	if hostIsInternal(host) {
		return "", fmt.Errorf("доступ к внутренним/приватным адресам запрещён: %s", host)
	}
	return u.String(), nil
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
	// A DNS name: block if ANY resolved address is internal. If it doesn't resolve, it can't
	// be reached, so let the later fetch fail naturally.
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
