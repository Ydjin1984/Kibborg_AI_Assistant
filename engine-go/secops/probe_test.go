package secops

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProbeURL_FindsMissingHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.env":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("SECRET=1"))
		case "/robots.txt":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("User-agent: *\nDisallow:\n"))
		default:
			w.Header().Set("Server", "TestServer/1.0")
			w.Header().Set("Set-Cookie", "sid=abc; Path=/")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		}
	}))
	defer srv.Close()

	// httptest uses 127.0.0.1 — blocked by SSRF guard. Probe the guard separately;
	// for header logic, call analyzeProbe via a public-looking path isn't possible here.
	// So we unit-test analyzeProbe with a crafted response.
	_ = srv

	rep := URLProbe{URL: "https://example.com/", FinalURL: "https://example.com/", HTTPS: true}
	resp := &http.Response{Header: http.Header{}, Request: &http.Request{}}
	resp.Header.Set("Server", "nginx")
	findings := analyzeProbe(rep, resp)
	joined := ""
	for _, f := range findings {
		joined += f.ID + " "
	}
	for _, need := range []string{"missing-hsts", "missing-csp", "missing-xcto", "clickjacking"} {
		if !strings.Contains(joined, need) {
			t.Errorf("ожидали finding %s, получили %q", need, joined)
		}
	}
}

func TestSafePublicURL_BlocksPrivate(t *testing.T) {
	for _, u := range []string{
		"http://127.0.0.1/",
		"http://localhost/admin",
		"http://192.168.0.1/",
		"file:///etc/passwd",
		"http://2130706433/",
		"http://0x7f000001/",
	} {
		if _, err := safePublicURL(u); err == nil {
			t.Errorf("ожидали блок %s", u)
		}
	}
}

func TestSafePublicURL_AllowsPublic(t *testing.T) {
	got, err := safePublicURL("https://example.com/path")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "https://example.com") {
		t.Fatalf("unexpected %q", got)
	}
}

func TestSameSPAShell(t *testing.T) {
	home := responseFingerprint{Status: 200, Type: "text/html; charset=utf-8", Len: "7000", Prefix: "<!DOCTYPE html><html>"}
	same := responseFingerprint{Status: 200, Type: "text/html; charset=utf-8", Len: "7000", Prefix: "<!DOCTYPE html><html>"}
	real := responseFingerprint{Status: 200, Type: "text/plain", Len: "42", Prefix: "SECRET=1"}
	if !sameSPAShell(home, same) {
		t.Fatal("identical SPA shell must match")
	}
	if sameSPAShell(home, real) {
		t.Fatal("plain secret body must not match SPA shell")
	}
}
