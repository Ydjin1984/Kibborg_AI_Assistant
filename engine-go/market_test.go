package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNormalizeSymbol(t *testing.T) {
	cases := map[string]string{
		"btc":      "BTCUSDT",
		"ETH":      "ETHUSDT",
		"sol/usdt": "SOLUSDT",
		"BTC-USDT": "BTCUSDT",
		"  eth ":   "ETHUSDT",
		"btcusdt":  "BTCUSDT",
		"BNBBTC":   "BNBBTC",  // known quote suffix → left as-is
		"sol\n":    "SOLUSDT", // trailing newline must not reach Binance
		"do ge":    "DOGEUSDT",
		"$sol!":    "SOLUSDT", // stray punctuation stripped
		"ВТС":      "",        // all-Cyrillic → empty (caller shows "укажи тикер", never hits Binance)
		"btС":      "BTUSDT",  // mixed: Cyrillic С dropped, Latin BT kept — no illegal chars sent
		"":         "",
	}
	for in, want := range cases {
		if got := normalizeSymbol(in); got != want {
			t.Errorf("normalizeSymbol(%q) = %q, want %q", in, got, want)
		}
	}
}

func sampleKlineJSON() string {
	return `[[1710000000000,"1","2","0.5","1.5","10",1710000000001]]`
}

func TestParseKlineUnknownSymbol(t *testing.T) {
	_, err := parseKlineRows([]byte(`{"code":-1121,"msg":"Invalid symbol."}`), 400, "ZZZUSDT", "1h")
	if !isUnknownBinanceSymbol(err) {
		t.Fatalf("want unknown-symbol error, got %v", err)
	}
}

func TestKlineFallbackUsesSecondHost(t *testing.T) {
	t.Cleanup(func() { rememberKlineHost("") })
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusBadGateway)
	}))
	defer dead.Close()
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.RawQuery, "symbol=BNBUSDT") {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		_, _ = io.WriteString(w, sampleKlineJSON())
	}))
	defer live.Close()

	rows, err := fetchKlineRowsFrom([]string{dead.URL, live.URL}, "BNBUSDT", "15m", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d", len(rows))
	}
	klineHostMu.Lock()
	pref := klineHostPref
	klineHostMu.Unlock()
	if pref != live.URL {
		t.Errorf("preferred host = %q, want live", pref)
	}
}

func TestKlineUnknownSymbolDoesNotFallback(t *testing.T) {
	t.Cleanup(func() { rememberKlineHost("") })
	hits := 0
	unknown := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"code":-1121,"msg":"Invalid symbol."}`)
	}))
	defer unknown.Close()
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("second host must not be called for unknown symbol")
	}))
	defer live.Close()

	_, err := fetchKlineRowsFrom([]string{unknown.URL, live.URL}, "NOSUCH", "15m", 1)
	if !isUnknownBinanceSymbol(err) {
		t.Fatalf("got %v", err)
	}
	if hits != 1 {
		t.Errorf("unknown host hits = %d", hits)
	}
}

func TestKlineAllHostsFail(t *testing.T) {
	t.Cleanup(func() { rememberKlineHost("") })
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer dead.Close()
	_, err := fetchKlineRowsFrom([]string{dead.URL}, "BNBUSDT", "15m", 1)
	if err == nil || !strings.Contains(err.Error(), "binance недоступен") {
		t.Fatalf("got %v", err)
	}
}

func TestKlineHostOrderPrefersLastGood(t *testing.T) {
	klineHostMu.Lock()
	origHosts, origPref := klineHosts, klineHostPref
	klineHosts = []string{"https://a.example", "https://b.example"}
	klineHostPref = "https://b.example"
	klineHostMu.Unlock()
	t.Cleanup(func() {
		klineHostMu.Lock()
		klineHosts, klineHostPref = origHosts, origPref
		klineHostMu.Unlock()
	})
	got := klineHostOrder()
	if got[0] != "https://b.example" || got[1] != "https://a.example" {
		t.Errorf("order = %v", got)
	}
}
