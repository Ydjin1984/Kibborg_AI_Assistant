package trading

import "testing"

// tfd builds one timeframe's data map the way market.go produces it.
func tfd(trend string, close, atr, changePct, vol, volSma float64) map[string]interface{} {
	return map[string]interface{}{
		"close":        close,
		"atr14":        atr,
		"change_pct":   changePct,
		"volume":       vol,
		"volume_sma20": volSma,
		"trend":        trend,
	}
}

func frames(t15, t1h, t4h string, atr float64) map[string]interface{} {
	return map[string]interface{}{
		"15m": tfd(t15, 100, atr, 0.5, 100, 100),
		"1h":  tfd(t1h, 100, atr, 0.5, 100, 100),
		"4h":  tfd(t4h, 100, atr, 0.5, 100, 100),
	}
}

func TestClassifyRegimeNoData(t *testing.T) {
	r := ClassifyRegime(map[string]interface{}{})
	if r.Status != "NO_DATA" || r.Regime != "unknown" || r.Confidence != 0 {
		t.Errorf("empty input: %+v", r)
	}
	// Frames without "close" are unusable too — never a confident answer on missing data.
	r = ClassifyRegime(map[string]interface{}{"15m": map[string]interface{}{"trend": "bullish"}})
	if r.Status != "NO_DATA" {
		t.Errorf("frames without close should be NO_DATA, got %s", r.Status)
	}
}

func TestClassifyRegimeTrends(t *testing.T) {
	r := ClassifyRegime(frames("bullish", "bullish", "bullish", 1))
	if r.Regime != "trend_up" {
		t.Errorf("all bullish → %q, want trend_up", r.Regime)
	}
	if r.Confidence < 0.78 {
		t.Errorf("3/3 bullish confidence = %v, want boosted ≥ 0.78", r.Confidence)
	}
	r = ClassifyRegime(frames("bearish", "bearish", "bearish", 1))
	if r.Regime != "trend_down" {
		t.Errorf("all bearish → %q, want trend_down", r.Regime)
	}
	// 2 bullish but 4h NOT confirming → no trend_up.
	r = ClassifyRegime(frames("bullish", "bullish", "range", 1))
	if r.Regime == "trend_up" {
		t.Errorf("4h must confirm the trend, got %q", r.Regime)
	}
	r = ClassifyRegime(frames("range", "range", "range", 1))
	if r.Regime != "range" {
		t.Errorf("all range → %q, want range", r.Regime)
	}
	r = ClassifyRegime(frames("bullish", "bearish", "range", 1))
	if r.Regime != "transition" {
		t.Errorf("disagreeing frames → %q, want transition", r.Regime)
	}
}

func TestClassifyRegimeVolatileAndPanic(t *testing.T) {
	// atr 4 on price 100 → 4% ≥ 3.5 → volatile.
	r := ClassifyRegime(frames("bullish", "bullish", "bullish", 4))
	if r.Regime != "volatile" {
		t.Errorf("elevated ATR → %q, want volatile", r.Regime)
	}
	// ATR 6% + volume spike + big move → panic overrides everything.
	tf := map[string]interface{}{
		"15m": tfd("bullish", 100, 6, 3.0, 250, 100),
		"1h":  tfd("bullish", 100, 6, 3.0, 250, 100),
		"4h":  tfd("bullish", 100, 6, 3.0, 250, 100),
	}
	r = ClassifyRegime(tf)
	if r.Regime != "panic" {
		t.Errorf("vol/volume shock → %q, want panic", r.Regime)
	}
	// High-risk event forces panic even on calm data.
	r = ClassifyRegime(frames("range", "range", "range", 1),
		map[string]interface{}{"event_context": map[string]interface{}{"status": "HIGH_RISK"}})
	if r.Regime != "panic" {
		t.Errorf("HIGH_RISK event → %q, want panic", r.Regime)
	}
}

func TestClassifyRegimeSqueeze(t *testing.T) {
	r := ClassifyRegime(frames("range", "range", "range", 1),
		map[string]interface{}{"funding_context": map[string]interface{}{"last_funding_rate": 0.002}})
	if r.Regime != "squeeze" {
		t.Errorf("extreme funding → %q, want squeeze", r.Regime)
	}
}

func TestClassifyRegimeConfidenceCap(t *testing.T) {
	for _, f := range [][3]string{
		{"bullish", "bullish", "bullish"},
		{"bearish", "bearish", "bearish"},
		{"range", "range", "range"},
	} {
		r := ClassifyRegime(frames(f[0], f[1], f[2], 1))
		if r.Confidence < 0 || r.Confidence > 0.95 {
			t.Errorf("confidence %v out of [0, 0.95] for %v", r.Confidence, f)
		}
	}
}
