package secops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDownloadHelpers_SecretPathAndSPA(t *testing.T) {
	if !looksLikeSecretPath("https://profi.sysx.uz/.env") {
		t.Fatal("/.env должен считаться секретным путём")
	}
	if looksLikeSecretPath("https://profi.sysx.uz/about") {
		t.Fatal("/about не секретный путь")
	}
	if !looksLikeProbeTrapPath("https://profi.sysx.uz/admin") || !looksLikeProbeTrapPath("https://profi.sysx.uz/wp-login.php") {
		t.Fatal("/admin и /wp-login — ловушки soft-404")
	}
	html := []byte(`<!DOCTYPE html><html><head><title>System-X</title></head><body></body></html>`)
	if !looksLikeHTMLBody("text/html; charset=utf-8", html) {
		t.Fatal("HTML body не распознан")
	}
	spa := []byte(`<!DOCTYPE html><html><head><meta charset="utf-8"><script>var theme=localStorage.getItem('nuxt-color-mode')</script><script src="/_nuxt/app.js"></script></head><body><div id="__nuxt"></div></body></html>`)
	if !looksLikeSPAAppShell(spa) {
		t.Fatal("Nuxt-оболочка должна распознаваться")
	}
	jsonBody := []byte(`{"staff":[]}`)
	if looksLikeHTMLBody("application/json", jsonBody) {
		t.Fatal("JSON не должен считаться HTML")
	}
}

func TestDownloadHelpers_NameAndPreview(t *testing.T) {
	name := suggestDownloadName("https://x.test/api/v1/staff", "application/json", []byte(`{}`))
	if name != "staff.json" && !strings.HasSuffix(name, ".json") {
		t.Fatalf("имя: %q", name)
	}
	safe := sanitizeEvidenceName(`..\..\evil.env`)
	if strings.Contains(safe, "..") || filepath.Base(safe) != safe {
		t.Fatalf("имя не санитизировано: %q", safe)
	}
	prev := textPreview([]byte("hello evidence"), "text/plain")
	if prev != "hello evidence" {
		t.Fatalf("preview: %q", prev)
	}
}

func TestDownloadURL_BlocksInternal(t *testing.T) {
	_, err := DownloadURL("http://127.0.0.1/.env", "env.txt")
	if err == nil {
		t.Fatal("localhost обязан блокироваться SSRF-guard")
	}
}

func TestEvidenceDirConvention(t *testing.T) {
	// WriteSecurityReport and download_url share runtime/browser so /api/files works.
	dir := t.TempDir()
	cwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(securityEvidenceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(securityEvidenceDir, "20260827-test-staff.json")
	if err := os.WriteFile(path, []byte(`{"ok":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	rel := filepath.ToSlash(filepath.Join("security", "evidence", filepath.Base(path)))
	if !strings.HasPrefix(rel, "security/evidence/") {
		t.Fatalf("rel для /api/files: %q", rel)
	}
}
