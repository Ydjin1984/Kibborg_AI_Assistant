package trading

import "testing"

func TestAnalyzeSymbolBearish(t *testing.T) {
	r := AnalyzeSymbol("BTCUSDT", "spot", false, frames("bearish", "bearish", "bearish", 1), nil,
		[]string{"regime_classifier", "scoring"})
	if r.Symbol != "BTCUSDT" || r.Market != "spot" {
		t.Errorf("identity fields: %s %s", r.Symbol, r.Market)
	}
	if r.Regime != "trend_down" {
		t.Errorf("regime = %q, want trend_down", r.Regime)
	}
	if r.Direction != "short" {
		t.Errorf("direction = %q, want short (from trend_down regime)", r.Direction)
	}
	if r.Confidence < 0.05 || r.Confidence > 0.92 {
		t.Errorf("confidence %v out of calibrated bounds [0.05, 0.92]", r.Confidence)
	}
	if r.Probability < 0.1 || r.Probability > 0.95 {
		t.Errorf("probability %v out of [0.1, 0.95]", r.Probability)
	}
	used, _ := r.Meta["modules_used"].([]string)
	if len(used) != 2 {
		t.Errorf("modules_used = %v", used)
	}
}

func TestAnalyzeSymbolRange(t *testing.T) {
	r := AnalyzeSymbol("ETHUSDT", "spot", false, frames("range", "range", "range", 1), nil, nil)
	if r.Regime != "range" {
		t.Errorf("regime = %q", r.Regime)
	}
	if r.Direction != "wait/range" {
		t.Errorf("direction = %q, want wait/range", r.Direction)
	}
}

func TestAnalyzeSymbolModulesDisabled(t *testing.T) {
	r := AnalyzeSymbol("SOLUSDT", "spot", false, frames("bullish", "bullish", "bullish", 1), nil,
		[]string{"scoring"})
	if r.Regime != "user_disabled" {
		t.Errorf("disabled regime module → %q", r.Regime)
	}
	used, _ := r.Meta["modules_used"].([]string)
	if len(used) != 1 || used[0] != "scoring" {
		t.Errorf("modules_used = %v, want [scoring]", used)
	}
}

func TestAnalyzeSymbolPlanAndDecision(t *testing.T) {
	// All-bullish → trend_up → long, so the engine must produce concrete, valid levels.
	r := AnalyzeSymbol("BTCUSDT", "spot", false, frames("bullish", "bullish", "bullish", 1), nil, nil)
	if r.Direction != "long" {
		t.Fatalf("direction = %q, want long", r.Direction)
	}
	entry, ok := r.Plan["entry"].(float64)
	if !ok || entry <= 0 {
		t.Errorf("plan must carry a positive entry, got %v", r.Plan["entry"])
	}
	stop, _ := r.Plan["stop"].(float64)
	if stop >= entry {
		t.Errorf("long stop %v must sit below entry %v", stop, entry)
	}
	if _, ok := r.Plan["rr"].(float64); !ok {
		t.Errorf("a valid long plan must expose an R/R, plan=%v", r.Plan)
	}
	decision, _ := r.Meta["decision"].(string)
	if decision != "ALLOW" && decision != "WATCH" && decision != "WAIT" {
		t.Errorf("unexpected decision %q", decision)
	}
	if _, ok := r.Meta["confidence_calibration"].(map[string]interface{}); !ok {
		t.Error("Meta.confidence_calibration must be present")
	}
}

func TestAnalyzeSymbolVolatileStaysNeutral(t *testing.T) {
	// A volatile regime (high ATR) is ambiguous: the engine must NOT default to long.
	r := AnalyzeSymbol("BTCUSDT", "spot", false, frames("bullish", "range", "bearish", 5), nil, nil)
	if r.Direction == "long" || r.Direction == "short" {
		t.Errorf("ambiguous regime %q took a side (%q) — should wait", r.Regime, r.Direction)
	}
	if r.Regime == "trend_up" || r.Regime == "trend_down" {
		t.Skip("regime resolved to a trend for this fixture; neutrality check n/a")
	}
}

func TestAnalyzeSymbolReportShape(t *testing.T) {
	r := AnalyzeSymbol("BTCUSDT", "spot", false, frames("bullish", "bullish", "bullish", 1), nil, nil)
	// The canonical report must always carry its structured sections.
	if r.Risk == nil || r.Plan == nil || r.DisciplineGuard == nil || r.DataQuality == nil {
		t.Error("report sections must never be nil")
	}
	if _, ok := r.Meta["scoring"].(ScoreBreakdown); !ok {
		t.Error("Meta.scoring must be a ScoreBreakdown")
	}
	if _, ok := r.Meta["regime"].(RegimeResult); !ok {
		t.Error("Meta.regime must be a RegimeResult")
	}
	if r.Timestamp.IsZero() {
		t.Error("timestamp must be set")
	}
}
