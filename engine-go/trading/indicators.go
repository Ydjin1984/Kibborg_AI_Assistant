package trading

// Deterministic indicator math over candle series. Pure functions — no network, no state —
// so the "LLM never invents numbers" invariant holds end-to-end: main fetches candles,
// these functions turn them into the metrics ClassifyRegime/scoring consume.

import "math"

// EMA returns the exponential moving average series (same length as values). The first
// period-1 entries are a running mean warm-up; from index period-1 on it's the classic
// SMA-seeded EMA. Returns nil when there is not enough data.
func EMA(values []float64, period int) []float64 {
	if period <= 0 || len(values) < period {
		return nil
	}
	out := make([]float64, len(values))
	sum := 0.0
	for i := 0; i < period; i++ {
		sum += values[i]
		out[i] = sum / float64(i+1)
	}
	k := 2.0 / float64(period+1)
	for i := period; i < len(values); i++ {
		out[i] = values[i]*k + out[i-1]*(1-k)
	}
	return out
}

// RSI computes Wilder's Relative Strength Index over closes and returns the latest value.
// Not enough data → neutral 50 (never a made-up extreme).
func RSI(closes []float64, period int) float64 {
	s, from := RSISeries(closes, period)
	if s == nil || from < 0 || from >= len(s) {
		return 50
	}
	return s[len(s)-1]
}

// RSISeries returns the full Wilder RSI aligned with closes. Values before `from` are 0
// and must not be read — a real RSI can be 0, so the caller uses `from`, not "nonzero".
// Insufficient data → (nil, -1).
func RSISeries(closes []float64, period int) ([]float64, int) {
	if period <= 0 || len(closes) < period+1 {
		return nil, -1
	}
	n := len(closes)
	out := make([]float64, n)
	var gain, loss float64
	for i := 1; i <= period; i++ {
		d := closes[i] - closes[i-1]
		if d > 0 {
			gain += d
		} else {
			loss -= d
		}
	}
	avgGain := gain / float64(period)
	avgLoss := loss / float64(period)
	out[period] = rsiFromAvg(avgGain, avgLoss)
	for i := period + 1; i < n; i++ {
		d := closes[i] - closes[i-1]
		g, l := 0.0, 0.0
		if d > 0 {
			g = d
		} else {
			l = -d
		}
		avgGain = (avgGain*float64(period-1) + g) / float64(period)
		avgLoss = (avgLoss*float64(period-1) + l) / float64(period)
		out[i] = rsiFromAvg(avgGain, avgLoss)
	}
	return out, period
}

func rsiFromAvg(avgGain, avgLoss float64) float64 {
	if avgLoss == 0 {
		if avgGain == 0 {
			return 50
		}
		return 100
	}
	return 100 - 100/(1+avgGain/avgLoss)
}

// ATR computes Wilder's Average True Range and returns the latest value (0 = no data).
func ATR(highs, lows, closes []float64, period int) float64 {
	n := len(closes)
	if period <= 0 || n < period+1 || len(highs) != n || len(lows) != n {
		return 0
	}
	trs := make([]float64, 0, n-1)
	for i := 1; i < n; i++ {
		tr := highs[i] - lows[i]
		if v := math.Abs(highs[i] - closes[i-1]); v > tr {
			tr = v
		}
		if v := math.Abs(lows[i] - closes[i-1]); v > tr {
			tr = v
		}
		trs = append(trs, tr)
	}
	atr := 0.0
	for i := 0; i < period; i++ {
		atr += trs[i]
	}
	atr /= float64(period)
	for i := period; i < len(trs); i++ {
		atr = (atr*float64(period-1) + trs[i]) / float64(period)
	}
	return atr
}

// MACDHist returns the latest MACD(12,26,9) histogram value (0 = not enough data).
func MACDHist(closes []float64) float64 {
	const fast, slow, signal = 12, 26, 9
	if len(closes) < slow+signal {
		return 0
	}
	emaFast := EMA(closes, fast)
	emaSlow := EMA(closes, slow)
	macd := make([]float64, 0, len(closes)-slow+1)
	for i := slow - 1; i < len(closes); i++ {
		macd = append(macd, emaFast[i]-emaSlow[i])
	}
	sig := EMA(macd, signal)
	if sig == nil {
		return 0
	}
	return macd[len(macd)-1] - sig[len(sig)-1]
}

// EMALast returns just the latest EMA value (0 = not enough data).
func EMALast(values []float64, period int) float64 {
	e := EMA(values, period)
	if e == nil {
		return 0
	}
	return e[len(e)-1]
}

// SwingStructure classifies market structure from swing highs/lows over the recent window:
// "up" (higher-high + higher-low), "down" (lower-high + lower-low), else "choppy".
// It compares the most recent half of the window against the half before it.
func SwingStructure(highs, lows []float64) string {
	n := len(highs)
	if n < 20 || len(lows) != n {
		return "choppy"
	}
	win := 40
	if win > n {
		win = n
	}
	seg := win / 2
	recentHi, recentLo := sliceMax(highs[n-seg:]), sliceMin(lows[n-seg:])
	olderHi, olderLo := sliceMax(highs[n-2*seg:n-seg]), sliceMin(lows[n-2*seg:n-seg])
	hh, hl := recentHi > olderHi, recentLo > olderLo
	lh, ll := recentHi < olderHi, recentLo < olderLo
	switch {
	case hh && hl:
		return "up"
	case lh && ll:
		return "down"
	default:
		return "choppy"
	}
}

func sliceMax(v []float64) float64 {
	m := v[0]
	for _, x := range v[1:] {
		if x > m {
			m = x
		}
	}
	return m
}

func sliceMin(v []float64) float64 {
	m := v[0]
	for _, x := range v[1:] {
		if x < m {
			m = x
		}
	}
	return m
}

// SMALast returns the simple moving average of the last period values (0 = no data).
func SMALast(values []float64, period int) float64 {
	if period <= 0 || len(values) < period {
		return 0
	}
	sum := 0.0
	for _, v := range values[len(values)-period:] {
		sum += v
	}
	return sum / float64(period)
}

// TrendLabel classifies a close series as bullish / bearish / range using the classic
// price vs EMA20 vs EMA50 alignment — the labels ClassifyRegime and scoring expect.
func TrendLabel(closes []float64) string {
	if len(closes) < 50 {
		return "range"
	}
	e20 := EMA(closes, 20)
	e50 := EMA(closes, 50)
	price := closes[len(closes)-1]
	a, b := e20[len(e20)-1], e50[len(e50)-1]
	switch {
	case price > a && a > b:
		return "bullish"
	case price < a && a < b:
		return "bearish"
	default:
		return "range"
	}
}
