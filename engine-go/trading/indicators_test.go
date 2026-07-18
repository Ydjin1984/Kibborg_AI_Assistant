package trading

import (
	"math"
	"testing"
)

func rising(n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = 100 + float64(i)
	}
	return out
}

func falling(n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = 200 - float64(i)
	}
	return out
}

func flat(n int, v float64) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func TestEMA(t *testing.T) {
	if got := EMA([]float64{1, 2}, 5); got != nil {
		t.Errorf("EMA with insufficient data should be nil, got %v", got)
	}
	// A constant series has a constant EMA.
	ema := EMA(flat(30, 42), 10)
	if math.Abs(ema[len(ema)-1]-42) > 1e-9 {
		t.Errorf("EMA of constant series = %v", ema[len(ema)-1])
	}
	// EMA follows a rising series upward and lags below the last price.
	ema = EMA(rising(60), 20)
	last := ema[len(ema)-1]
	if last <= ema[0] || last >= 159 {
		t.Errorf("EMA of rising series: first %.2f last %.2f", ema[0], last)
	}
}

func TestRSIBounds(t *testing.T) {
	if got := RSI(rising(40), 14); got != 100 {
		t.Errorf("RSI of strictly rising series = %.2f, want 100", got)
	}
	if got := RSI(falling(40), 14); got != 0 {
		t.Errorf("RSI of strictly falling series = %.2f, want 0", got)
	}
	if got := RSI(flat(40, 100), 14); got != 50 {
		t.Errorf("RSI of flat series = %.2f, want 50", got)
	}
	// Not enough data → neutral, never an invented extreme.
	if got := RSI([]float64{1, 2, 3}, 14); got != 50 {
		t.Errorf("RSI with insufficient data = %.2f, want 50", got)
	}
}

func TestATR(t *testing.T) {
	// Constant candles with a fixed 2-point range → ATR is exactly 2.
	n := 30
	highs := flat(n, 102)
	lows := flat(n, 100)
	closes := flat(n, 101)
	if got := ATR(highs, lows, closes, 14); math.Abs(got-2) > 1e-9 {
		t.Errorf("ATR = %v, want 2", got)
	}
	if got := ATR(nil, nil, nil, 14); got != 0 {
		t.Errorf("ATR without data = %v, want 0", got)
	}
}

func TestMACDHistSign(t *testing.T) {
	// A fresh breakout after a flat base: MACD crosses above its signal → positive histogram.
	// (A STEADY trend is the wrong fixture here — near its tail the histogram legitimately
	// flips sign as momentum stops accelerating.)
	up := append(flat(40, 100), rising(40)...)
	if got := MACDHist(up); got <= 0 {
		t.Errorf("MACD histogram of fresh breakout up = %v, want > 0", got)
	}
	down := append(flat(40, 200), falling(40)...)
	if got := MACDHist(down); got >= 0 {
		t.Errorf("MACD histogram of fresh breakdown = %v, want < 0", got)
	}
	if got := MACDHist(flat(10, 1)); got != 0 {
		t.Errorf("MACD with insufficient data = %v, want 0", got)
	}
}

func TestSMALast(t *testing.T) {
	if got := SMALast([]float64{1, 2, 3, 4}, 2); got != 3.5 {
		t.Errorf("SMALast = %v, want 3.5", got)
	}
	if got := SMALast([]float64{1}, 5); got != 0 {
		t.Errorf("SMALast insufficient = %v, want 0", got)
	}
}

func TestSwingStructure(t *testing.T) {
	// A clean uptrend (rising highs and lows) → "up".
	up := make([]float64, 60)
	upLo := make([]float64, 60)
	for i := range up {
		up[i] = 100 + float64(i)
		upLo[i] = 99 + float64(i)
	}
	if got := SwingStructure(up, upLo); got != "up" {
		t.Errorf("rising highs/lows = %q, want up", got)
	}
	// A clean downtrend → "down".
	dn := make([]float64, 60)
	dnLo := make([]float64, 60)
	for i := range dn {
		dn[i] = 200 - float64(i)
		dnLo[i] = 199 - float64(i)
	}
	if got := SwingStructure(dn, dnLo); got != "down" {
		t.Errorf("falling highs/lows = %q, want down", got)
	}
	// Flat/oscillating → "choppy".
	flatH := flat(60, 101)
	flatL := flat(60, 100)
	if got := SwingStructure(flatH, flatL); got != "choppy" {
		t.Errorf("flat = %q, want choppy", got)
	}
	// Too little data → choppy, never a made-up call.
	if got := SwingStructure(flat(5, 1), flat(5, 1)); got != "choppy" {
		t.Errorf("short series = %q, want choppy", got)
	}
}

func TestEMALast(t *testing.T) {
	if got := EMALast([]float64{1, 2}, 5); got != 0 {
		t.Errorf("EMALast insufficient = %v, want 0", got)
	}
	if got := EMALast(flat(30, 7), 10); math.Abs(got-7) > 1e-9 {
		t.Errorf("EMALast of constant = %v, want 7", got)
	}
}

func TestTrendLabel(t *testing.T) {
	if got := TrendLabel(rising(60)); got != "bullish" {
		t.Errorf("rising trend = %q", got)
	}
	if got := TrendLabel(falling(60)); got != "bearish" {
		t.Errorf("falling trend = %q", got)
	}
	if got := TrendLabel(flat(60, 100)); got != "range" {
		t.Errorf("flat trend = %q", got)
	}
	if got := TrendLabel(rising(10)); got != "range" {
		t.Errorf("short series must default to range, got %q", got)
	}
}
