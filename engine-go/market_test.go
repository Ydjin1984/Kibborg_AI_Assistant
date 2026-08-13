package main

import "testing"

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
