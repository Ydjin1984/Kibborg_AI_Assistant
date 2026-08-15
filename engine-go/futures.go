package main

// Binance USDT-M Futures public data (no key) that enriches the deterministic analysis with
// derivatives context: funding rate, open-interest change, and taker order-flow bias. These
// feed trading.ClassifyRegime's optional contexts, unlocking the squeeze and panic regimes.
// Everything here is BEST-EFFORT: a coin without a perpetual (or a transient API error) just
// yields a partial/empty context and the analysis proceeds on candles alone.

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"sync"
	"time"
)

var (
	fapiHostMu   sync.Mutex
	fapiHostPref string
)

var futuresHTTP = newBinanceHTTP(8 * time.Second)

// fapiHosts — те же зеркала, что у спота: один fapi.binance.com из РФ часто молчит.
var fapiHosts = []string{
	"https://fapi.binance.com",
	"https://fapi1.binance.com",
	"https://fapi2.binance.com",
	"https://fapi3.binance.com",
}

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
	if !getFapiJSON("/fapi/v1/premiumIndex?symbol="+symbol, &v) {
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
	if !getFapiJSON("/futures/data/openInterestHist?symbol="+symbol+"&period=5m&limit=4", &rows) {
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
	if !getFapiJSON("/futures/data/takerlongshortRatio?symbol="+symbol+"&period=5m&limit=1", &rows) {
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

func fapiHostOrder() []string {
	fapiHostMu.Lock()
	pref := fapiHostPref
	fapiHostMu.Unlock()
	if pref == "" {
		return append([]string(nil), fapiHosts...)
	}
	out := []string{pref}
	for _, h := range fapiHosts {
		if h != pref {
			out = append(out, h)
		}
	}
	return out
}

func getFapiJSON(path string, out any) bool {
	if path == "" || path[0] != '/' {
		path = "/" + path
	}
	for _, host := range fapiHostOrder() {
		if getFuturesJSON(host+path, out) {
			fapiHostMu.Lock()
			fapiHostPref = host
			fapiHostMu.Unlock()
			return true
		}
	}
	return false
}

// FlowBar — синтетическая свеча ряда (OI или CVD). Фитиль честный только как
// max/min open/close: у Binance нет OHLC по интересу, выдумывать тень нельзя.
type FlowBar struct {
	I int     `json:"i"`
	T int64   `json:"t"`
	O float64 `json:"o"`
	H float64 `json:"h"`
	L float64 `json:"l"`
	C float64 `json:"c"`
	D float64 `json:"d,omitempty"` // приращение бара (для CVD = buy−sell)
}

func klineIntervalMs(tf string) int64 {
	switch tf {
	case "5m":
		return 5 * 60 * 1000
	case "15m":
		return 15 * 60 * 1000
	case "30m":
		return 30 * 60 * 1000
	case "1h":
		return 60 * 60 * 1000
	case "4h":
		return 4 * 60 * 60 * 1000
	case "1d":
		return 24 * 60 * 60 * 1000
	default:
		return 0
	}
}

// FundingPrint — один факт фандинга, уже привязанный к свече графика.
// Rate как в API (0.0001 = 0.01%). Index = номер бара, на котором это произошло.
type FundingPrint struct {
	Time  int64   `json:"t"`
	Rate  float64 `json:"rate"`
	Index int     `json:"i"`
}

type fundingEvent struct {
	Time int64
	Rate float64
}

// chartFundingTFs — на этих ТФ фандинг виден как отдельное событие.
// На 1h/4h/1d свеча его проглатывает, и вертикаль только шумит.
func chartFundingInterval(tf string) int64 {
	switch tf {
	case "5m", "15m", "30m":
		return klineIntervalMs(tf)
	default:
		return 0
	}
}

// fetchFundingHistory — фактические выплаты с фьючерсного API, не сетка «каждые 8 часов».
// У части контрактов интервал уже 4h/1h; выдуманная сетка ставила бы метки мимо.
func fetchFundingHistory(symbol string, startMs, endMs int64) []fundingEvent {
	if symbol == "" || startMs <= 0 || endMs <= startMs {
		return nil
	}
	path := fmt.Sprintf("/fapi/v1/fundingRate?symbol=%s&startTime=%d&endTime=%d&limit=100",
		symbol, startMs, endMs)
	var rows []struct {
		FundingTime int64  `json:"fundingTime"`
		FundingRate string `json:"fundingRate"`
	}
	if !getFapiJSON(path, &rows) {
		return nil
	}
	out := make([]fundingEvent, 0, len(rows))
	for _, r := range rows {
		rate, err := strconv.ParseFloat(r.FundingRate, 64)
		if err != nil || r.FundingTime <= 0 {
			continue
		}
		out = append(out, fundingEvent{Time: r.FundingTime, Rate: rate})
	}
	return out
}

func fundingMarksForBars(symbol, interval string, opens []int64) []FundingPrint {
	step := chartFundingInterval(interval)
	if step <= 0 || len(opens) == 0 {
		return nil
	}
	start, end := opens[0], opens[len(opens)-1]+step
	return mapFundingToBars(fetchFundingHistory(symbol, start, end), opens, step)
}

func mapFundingToBars(events []fundingEvent, opens []int64, intervalMs int64) []FundingPrint {
	if intervalMs <= 0 || len(opens) == 0 || len(events) == 0 {
		return nil
	}
	out := make([]FundingPrint, 0, len(events))
	for _, ev := range events {
		i := fundingBarIndex(opens, ev.Time, intervalMs)
		if i < 0 {
			continue
		}
		out = append(out, FundingPrint{Time: ev.Time, Rate: ev.Rate, Index: i})
	}
	return out
}

// fundingBarIndex — свеча, внутри которой произошла выплата: open ≤ t < open+interval.
func fundingBarIndex(opens []int64, t, intervalMs int64) int {
	if intervalMs <= 0 || len(opens) == 0 {
		return -1
	}
	lo, hi, ans := 0, len(opens)-1, -1
	for lo <= hi {
		mid := (lo + hi) / 2
		if opens[mid] <= t {
			ans = mid
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	if ans < 0 || t >= opens[ans]+intervalMs {
		return -1
	}
	return ans
}

type timedValue struct {
	T int64
	V float64
}

func fetchOIHist(symbol, period string, limit int) []timedValue {
	if limit < 2 {
		limit = 2
	}
	if limit > 500 {
		limit = 500
	}
	path := fmt.Sprintf("/futures/data/openInterestHist?symbol=%s&period=%s&limit=%d",
		symbol, period, limit)
	var rows []struct {
		SumOpenInterest string `json:"sumOpenInterest"`
		Timestamp       int64  `json:"timestamp"`
	}
	if !getFapiJSON(path, &rows) {
		return nil
	}
	out := make([]timedValue, 0, len(rows))
	for _, r := range rows {
		v, err := strconv.ParseFloat(r.SumOpenInterest, 64)
		if err != nil || r.Timestamp <= 0 {
			continue
		}
		out = append(out, timedValue{T: r.Timestamp, V: v})
	}
	return out
}

func fetchTakerDelta(symbol, period string, limit int) []timedValue {
	if limit < 2 {
		limit = 2
	}
	if limit > 500 {
		limit = 500
	}
	path := fmt.Sprintf("/futures/data/takerBuySellVol?symbol=%s&period=%s&limit=%d",
		symbol, period, limit)
	var rows []struct {
		BuyVol    string `json:"buyVol"`
		SellVol   string `json:"sellVol"`
		Timestamp int64  `json:"timestamp"`
	}
	if !getFapiJSON(path, &rows) {
		return nil
	}
	out := make([]timedValue, 0, len(rows))
	for _, r := range rows {
		b, e1 := strconv.ParseFloat(r.BuyVol, 64)
		s, e2 := strconv.ParseFloat(r.SellVol, 64)
		if e1 != nil || e2 != nil || r.Timestamp <= 0 {
			continue
		}
		out = append(out, timedValue{T: r.Timestamp, V: b - s})
	}
	return out
}

func valueAtBar(points []timedValue, open, intervalMs int64) (float64, bool) {
	if intervalMs <= 0 {
		return 0, false
	}
	found, ok := 0.0, false
	for _, p := range points {
		if p.T >= open && p.T < open+intervalMs {
			found, ok = p.V, true
		}
	}
	return found, ok
}

func buildLevelCandles(points []timedValue, opens []int64, intervalMs int64) []FlowBar {
	if len(opens) == 0 || len(points) == 0 {
		return nil
	}
	out := make([]FlowBar, 0, len(opens))
	prev, have := 0.0, false
	for i, open := range opens {
		v, ok := valueAtBar(points, open, intervalMs)
		if !ok {
			continue
		}
		o := v
		if have {
			o = prev
		}
		h, l := o, o
		if v > h {
			h = v
		}
		if v < l {
			l = v
		}
		out = append(out, FlowBar{I: i, T: open, O: o, H: h, L: l, C: v})
		prev, have = v, true
	}
	return out
}

func buildCVDCandles(deltas []timedValue, opens []int64, intervalMs int64) []FlowBar {
	if len(opens) == 0 || len(deltas) == 0 {
		return nil
	}
	out := make([]FlowBar, 0, len(opens))
	cvd := 0.0
	for i, open := range opens {
		d, ok := valueAtBar(deltas, open, intervalMs)
		if !ok {
			continue
		}
		o := cvd
		cvd += d
		h, l := o, o
		if cvd > h {
			h = cvd
		}
		if cvd < l {
			l = cvd
		}
		out = append(out, FlowBar{I: i, T: open, O: o, H: h, L: l, C: cvd, D: d})
	}
	return out
}

func lastChangePct(points []timedValue) (float64, bool) {
	if len(points) < 2 || points[len(points)-2].V == 0 {
		return 0, false
	}
	a, b := points[len(points)-2].V, points[len(points)-1].V
	return (b - a) / a * 100, true
}

func lastDelta(points []timedValue) (float64, bool) {
	if len(points) == 0 {
		return 0, false
	}
	return points[len(points)-1].V, true
}

// attachFlowToTimeframes пишет OI/CVD в уже собранные 15m/1h/4h.
// Запросы параллельны: три ТФ не должны складывать таймауты в очередь.
func attachFlowToTimeframes(symbol string, timeframes map[string]any) {
	if symbol == "" || timeframes == nil {
		return
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, tf := range []string{"15m", "1h", "4h"} {
		tf := tf
		wg.Add(1)
		go func() {
			defer wg.Done()
			oi := fetchOIHist(symbol, tf, 8)
			cvd := fetchTakerDelta(symbol, tf, 8)
			mu.Lock()
			defer mu.Unlock()
			m, ok := timeframes[tf].(map[string]any)
			if !ok || m == nil {
				return
			}
			if pct, ok := lastChangePct(oi); ok {
				m["oi_change_pct"] = pct
			}
			if d, ok := lastDelta(cvd); ok {
				m["cvd_delta"] = d
			}
		}()
	}
	wg.Wait()
}

func flowPaneForBars(symbol, interval string, opens []int64) (oi, cvd []FlowBar) {
	step := klineIntervalMs(interval)
	if step <= 0 || len(opens) == 0 {
		return nil, nil
	}
	limit := len(opens) + 4
	if limit > 500 {
		limit = 500
	}
	var wg sync.WaitGroup
	var oiPts, cvdPts []timedValue
	wg.Add(2)
	go func() { defer wg.Done(); oiPts = fetchOIHist(symbol, interval, limit) }()
	go func() { defer wg.Done(); cvdPts = fetchTakerDelta(symbol, interval, limit) }()
	wg.Wait()
	return buildLevelCandles(oiPts, opens, step), buildCVDCandles(cvdPts, opens, step)
}
