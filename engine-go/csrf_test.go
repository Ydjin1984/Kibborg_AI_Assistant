package main

import (
	"net/http"
	"net/http/httptest"
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
