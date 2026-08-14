package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSameOrigin_BlocksCrossSiteAndRebinding(t *testing.T) {
	cases := []struct {
		name   string
		host   string
		origin string
		refer  string
		want   bool
	}{
		{"same-origin POST", "127.0.0.1:8090", "http://127.0.0.1:8090", "", true},
		{"same-origin via localhost", "localhost:8090", "http://localhost:8090", "", true},
		{"no origin, loopback host", "127.0.0.1:8090", "", "", true},
		{"cross-site origin", "127.0.0.1:8090", "https://evil.com", "", false},
		{"cross-site referer", "127.0.0.1:8090", "", "https://evil.com/x", false},
		{"dns-rebinding host", "evil.com:8090", "http://evil.com:8090", "", false},
		{"origin host mismatch port", "127.0.0.1:8090", "http://127.0.0.1:9999", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/browser", nil)
			r.Host = c.host
			if c.origin != "" {
				r.Header.Set("Origin", c.origin)
			}
			if c.refer != "" {
				r.Header.Set("Referer", c.refer)
			}
			if got := sameOrigin(r); got != c.want {
				t.Errorf("sameOrigin=%v, ожидалось %v", got, c.want)
			}
		})
	}
}

// ТЗ §9: the agent-control endpoints MUST sit behind sameOriginGuard. Without it, any page
// open in the very Chrome the agent drives could flip the hands to `full` — the cheapest
// possible bypass of the whole of chapter 6. This test is the guard on that guard.
func TestAgentControlEndpointsAreCSRFGuarded(t *testing.T) {
	mux := newWebMux(Config{})
	for _, path := range []string{"/api/hands", "/api/stop", "/api/confirm", "/api/chat", "/api/browser", "/api/compact",
		"/api/models/download", "/api/models/assign", "/api/models/cancel", "/api/tts", "/api/speak"} {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		r.Host = "127.0.0.1:8090"
		r.Header.Set("Origin", "https://evil.example")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s: cross-origin дал %d, ждали 403", path, w.Code)
		}
	}
}

// The hands switch reads and writes through the API the Web toggle uses (§6.4).
func TestHandsEndpointReadsAndWrites(t *testing.T) {
	t.Cleanup(func() { setHandsMode("safe", "test-cleanup") })
	mux := newWebMux(Config{})

	post := func(body string) map[string]any {
		r := httptest.NewRequest(http.MethodPost, "/api/hands", strings.NewReader(body))
		r.Host = "127.0.0.1:8090"
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("POST /api/hands → %d", w.Code)
		}
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := post(`{"mode":"full"}`); got["full"] != true || got["mode"] != handsModeFull {
		t.Fatalf("переключение в full не сработало: %v", got)
	}
	if currentHandsMode() != handsModeFull {
		t.Fatal("состояние рук не применилось к процессу")
	}
	if got := post(`{"mode":"safe"}`); got["full"] != false {
		t.Fatalf("возврат в safe не сработал: %v", got)
	}
}

// /api/stop must answer honestly when there is nothing to stop (§4.2).
func TestStopEndpointWithoutTask(t *testing.T) {
	mux := newWebMux(Config{})
	r := httptest.NewRequest(http.MethodPost, "/api/stop", nil)
	r.Host = "127.0.0.1:8090"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("POST /api/stop → %d", w.Code)
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["stopped"] != false {
		t.Fatalf("без активной задачи ждали stopped=false, получили %v", out)
	}
}

// Channel parity for the WEB entry point (AGENTS.md, ТЗ §7 «текст и голос равноправны»).
//
// This is the regression that produced «я работаю в изолированной среде, у меня нет доступа к
// терминалу» in reply to a spoken «у тебя же есть консоль, сделай сам»: typed text went to the
// layered agent, while a voice.webm upload fell through to the plain streaming chat — a code
// path with no tools at all. No hands switch could fix that, because there were no hands.
//
// The test asserts the routing seam, not the LLM: every text that reaches routeWebMessage must
// end up somewhere that CAN act.
func TestWebVoiceAndTextShareOneRoute(t *testing.T) {
	// The agent route is chosen for ordinary free text…
	if !wantsToolAgent("убери надпись активация windows") {
		t.Fatal("обычный текст должен уходить в агента")
	}
	// …and a transcript is just text: the same predicate, the same route.
	transcript := "у тебя же есть консоль, сделай всё сам"
	if !wantsToolAgent(transcript) {
		t.Fatal("расшифровка голоса — такой же текст, и путь у неё тот же")
	}
	// Slash shortcuts must survive transcription too (§7: «ярлыки в те же паки»).
	if is, arg := parseCommand("/download https://youtu.be/x", downloadCommands); !is || arg == "" {
		t.Fatal("команды должны разбираться и в расшифровке голоса")
	}
	// A spoken «да» has to answer a pending confirmation rather than start a new task.
	if yes, ok := confirmWord("да"); !ok || !yes {
		t.Fatal("«да» голосом — это подтверждение")
	}
}

func TestSameOriginGuard_Returns403(t *testing.T) {
	called := false
	h := sameOriginGuard(func(w http.ResponseWriter, r *http.Request) { called = true })

	r := httptest.NewRequest(http.MethodPost, "/api/browser", nil)
	r.Host = "127.0.0.1:8090"
	r.Header.Set("Origin", "https://evil.com")
	w := httptest.NewRecorder()
	h(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("ожидался 403 для cross-origin, получили %d", w.Code)
	}
	if called {
		t.Error("хендлер не должен вызываться при cross-origin запросе")
	}
}
