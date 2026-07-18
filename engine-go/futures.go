package main

// Binance USDT-M Futures public data (no key) that enriches the deterministic analysis with
// derivatives context: funding rate, open-interest change, and taker order-flow bias. These
// feed trading.ClassifyRegime's optional contexts, unlocking the squeeze and panic regimes.
// Everything here is BEST-EFFORT: a coin without a perpetual (or a transient API error) just
// yields a partial/empty context and the analysis proceeds on candles alone.

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"
)

var futuresHTTP = &http.Client{Timeout: 8 * time.Second}

// fetchFuturesContext assembles the extra-context map ClassifyRegime consumes.
func fetchFuturesContext(symbol string) map[string]any {
	ctx := map[string]any{}
	if fr, ok := fetchFundingRate(symbol); ok {
		fc := map[string]any{"last_funding_rate": fr}
		if oiPct, ok := fetchOIChangePct(symbol); ok {
			fc["open_interest_change_15m_window_pct"] = oiPct
		}
		ctx["funding_context"] = fc
	}
	if bias, ok := fetchOrderflowBias(symbol); ok {
		ctx["orderflow"] = map[string]any{"bias": bias}
	}
	if len(ctx) == 0 {
		return nil
	}
	return ctx
}

// fetchFundingRate returns the latest perpetual funding rate (e.g. 0.0001 = 0.01%).
func fetchFundingRate(symbol string) (float64, bool) {
	var v struct {
		LastFundingRate string `json:"lastFundingRate"`
	}
	if !getFuturesJSON("https://fapi.binance.com/fapi/v1/premiumIndex?symbol="+symbol, &v) {
		return 0, false
	}
	f, err := strconv.ParseFloat(v.LastFundingRate, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

// fetchOIChangePct returns open-interest % change over the last ~15 minutes (4×5m points).
func fetchOIChangePct(symbol string) (float64, bool) {
	var rows []struct {
		SumOpenInterest string `json:"sumOpenInterest"`
	}
	if !getFuturesJSON("https://fapi.binance.com/futures/data/openInterestHist?symbol="+symbol+"&period=5m&limit=4", &rows) {
		return 0, false
	}
	if len(rows) < 2 {
		return 0, false
	}
	first, e1 := strconv.ParseFloat(rows[0].SumOpenInterest, 64)
	last, e2 := strconv.ParseFloat(rows[len(rows)-1].SumOpenInterest, 64)
	if e1 != nil || e2 != nil || first == 0 {
		return 0, false
	}
	return (last - first) / first * 100, true
}

// fetchOrderflowBias derives a directional bias from the taker buy/sell volume ratio.
func fetchOrderflowBias(symbol string) (string, bool) {
	var rows []struct {
		BuySellRatio string `json:"buySellRatio"`
	}
	if !getFuturesJSON("https://fapi.binance.com/futures/data/takerlongshortRatio?symbol="+symbol+"&period=5m&limit=1", &rows) {
		return "", false
	}
	if len(rows) == 0 {
		return "", false
	}
	r, err := strconv.ParseFloat(rows[0].BuySellRatio, 64)
	if err != nil {
		return "", false
	}
	return orderflowBiasFromRatio(r), true
}

// orderflowBiasFromRatio maps a taker buy/sell ratio to a bias label. Pure (unit-testable).
func orderflowBiasFromRatio(r float64) string {
	switch {
	case r >= 1.15:
		return "buy_pressure"
	case r > 0 && r <= 0.87:
		return "sell_pressure"
	default:
		return "neutral"
	}
}

func getFuturesJSON(url string, out any) bool {
	resp, err := futuresHTTP.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return false
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return json.Unmarshal(raw, out) == nil
}
