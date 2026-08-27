package browser

import (
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

func TestSafeRemoteURL_BlocksSSRFAndLFI(t *testing.T) {
	blocked := []string{
		"file:///C:/Users/me/.ssh/id_rsa",
		"chrome://settings",
		"data:text/html,<script>alert(1)</script>",
		"http://127.0.0.1/admin",
		"http://localhost:9222/json",
		"https://169.254.169.254/latest/meta-data/",
		"http://[::1]/",
		"http://10.0.0.5/internal",
		"http://192.168.1.1/",
		"http://metadata.internal/",
		"ftp://example.com/file",
		"http://2130706433/", // decimal loopback
		"http://0x7f000001/", // hex loopback
	}
	for _, u := range blocked {
		if _, err := safeRemoteURL(u); err == nil {
			t.Errorf("ожидалась блокировка %q, но URL прошёл", u)
		}
	}
}

func TestSafeHTTPClient_BlocksRedirectToInternal(t *testing.T) {
	client := safeHTTPClient(5 * time.Second)
	if client.CheckRedirect == nil {
		t.Fatal("CheckRedirect must be set")
	}
	req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1/secret", nil)
	via := []*http.Request{req}
	if err := client.CheckRedirect(req, via); err == nil {
		t.Fatal("редирект на 127.0.0.1 должен блокироваться")
	}
}

func TestSafeRemoteURL_AllowsPublic(t *testing.T) {
	// Prefer reserved/example hosts — avoid flaky third-party DNS in CI.
	allowed := []string{
		"https://example.com/page",
		"http://example.org/api",
		"https://example.net/watch?v=abc",
	}
	for _, u := range allowed {
		if _, err := safeRemoteURL(u); err != nil {
			t.Errorf("ожидался пропуск %q, но получили ошибку: %v", u, err)
		}
	}
}

func TestSafeArtifactPath_ConfinesToArtifactDir(t *testing.T) {
	root, _ := filepath.Abs(artifactDir)

	// Inside the artifact dir → allowed.
	inside := filepath.Join(root, "sub", "file.png")
	if got, err := safeArtifactPath(inside); err != nil {
		t.Errorf("путь внутри каталога должен быть разрешён: %v", err)
	} else if got != inside {
		t.Errorf("ожидался %q, получили %q", inside, got)
	}

	// Traversal / absolute escape → blocked.
	for _, p := range []string{
		filepath.Join(root, "..", "..", "secret.txt"),
		`C:\Windows\System32\config\SAM`,
		"/etc/passwd",
	} {
		if _, err := safeArtifactPath(p); err == nil {
			t.Errorf("ожидалась блокировка пути вне каталога: %q", p)
		}
	}
}
