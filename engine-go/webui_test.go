package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func webTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(newWebMux(Config{BrainPort: 1, WhisperPort: 1, WebPort: 8090}))
	t.Cleanup(srv.Close)
	return srv
}

func TestWebIndexServed(t *testing.T) {
	srv := webTestServer(t)
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET / = %d", resp.StatusCode)
	}
	buf := make([]byte, 4096)
	n, _ := resp.Body.Read(buf)
	if !strings.Contains(string(buf[:n]), "Kibborg") {
		t.Error("index page does not look like the dashboard")
	}
	// unknown paths must 404, not serve the index
	r2, err := http.Get(srv.URL + "/nope")
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Body.Close()
	if r2.StatusCode != 404 {
		t.Errorf("GET /nope = %d, want 404", r2.StatusCode)
	}
}

func TestWebStatusShape(t *testing.T) {
	srv := webTestServer(t)
	resp, err := http.Get(srv.URL + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var s map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"brain_ready", "whisper", "chrome_tabs", "chats", "uptime_sec"} {
		if _, ok := s[key]; !ok {
			t.Errorf("status is missing %q: %v", key, s)
		}
	}
	// with nothing running, brain must honestly report not-ready
	if s["brain_ready"] != false {
		t.Errorf("brain_ready = %v with no LLM on port 1", s["brain_ready"])
	}
}

func TestWebResetAndChatValidation(t *testing.T) {
	srv := webTestServer(t)
	// reset requires POST
	r, err := http.Get(srv.URL + "/api/reset")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET /api/reset = %d, want 405", r.StatusCode)
	}
	resp, err := http.Post(srv.URL+"/api/reset", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("POST /api/reset = %d", resp.StatusCode)
	}
	// chat without message → 400
	resp2, err := http.Post(srv.URL+"/api/chat", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Errorf("empty chat = %d, want 400", resp2.StatusCode)
	}
	// analyze without symbol → 400
	resp3, err := http.Get(srv.URL + "/api/analyze")
	if err != nil {
		t.Fatal(err)
	}
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusBadRequest {
		t.Errorf("analyze without symbol = %d, want 400", resp3.StatusCode)
	}
}
