package trading

import "testing"

func TestClassifyFlowSnapQuadrants(t *testing.T) {
	longs := ClassifyFlowSnap(FlowSnap{HasOI: true, HasCVD: true, PriceChange: 1, OIChangePct: 2, CVDDelta: 10})
	if longs.Quadrant != "new_longs" || longs.Side != "long" || !longs.CVDAgree {
		t.Errorf("new longs = %+v", longs)
	}
	shorts := ClassifyFlowSnap(FlowSnap{HasOI: true, HasCVD: true, PriceChange: -1, OIChangePct: 2, CVDDelta: -10})
	if shorts.Quadrant != "new_shorts" || shorts.Side != "short" {
		t.Errorf("new shorts = %+v", shorts)
	}
	cover := ClassifyFlowSnap(FlowSnap{HasOI: true, HasCVD: true, PriceChange: 1, OIChangePct: -2, CVDDelta: 5})
	if cover.Quadrant != "short_cover" || cover.Side != "" {
		t.Errorf("short cover must not be a long: %+v", cover)
	}
	against := ClassifyFlowSnap(FlowSnap{HasOI: true, HasCVD: true, PriceChange: 1, OIChangePct: 2, CVDDelta: -10})
	if against.Side != "" || against.Quadrant != "new_longs" {
		t.Errorf("CVD against must drop the side: %+v", against)
	}
}

func TestAnalyzeFlowTiebreak(t *testing.T) {
	snaps := []FlowSnap{
		{TF: "15m", HasOI: true, HasCVD: true, PriceChange: 0.4, OIChangePct: 1.1, CVDDelta: 8},
		{TF: "1h", HasOI: true, HasCVD: true, PriceChange: 0.6, OIChangePct: 0.8, CVDDelta: 12},
		{TF: "4h", HasOI: true, HasCVD: true, PriceChange: 0.2, OIChangePct: 0.5, CVDDelta: 3},
	}
	r := AnalyzeFlow(snaps)
	if !r.Available || r.Side != "long" || r.AllowShort {
		t.Fatalf("want long filter, got %+v", r)
	}
	if FlowTiebreak(snaps) != "long" {
		t.Error("tiebreak should pick long")
	}
	if FlowTiebreak(nil) != "" {
		t.Error("empty snaps must not invent a side")
	}
}

func TestAnalyzeFlowDisagreementIsNotASide(t *testing.T) {
	snaps := []FlowSnap{
		{TF: "15m", HasOI: true, HasCVD: true, PriceChange: 1, OIChangePct: 1, CVDDelta: 5},
		{TF: "1h", HasOI: true, HasCVD: true, PriceChange: -1, OIChangePct: 1, CVDDelta: -5},
		{TF: "4h", HasOI: true, HasCVD: true, PriceChange: 0.1, OIChangePct: -1, CVDDelta: 1},
	}
	r := AnalyzeFlow(snaps)
	if r.Side != "" {
		t.Errorf("mixed TFs must not pick a side, got %q", r.Side)
	}
}

func TestScoreLongUnchangedWithoutFlow(t *testing.T) {
	b := ScoreLong(scoringFrames(), map[string]interface{}{"direction": "long"})
	if len(b.Components) != 5 {
		t.Fatalf("without OI/CVD want 5 components, got %d", len(b.Components))
	}
}

func TestScoreLongBlendsFlow(t *testing.T) {
	fr := scoringFrames()
	for _, tf := range []string{"15m", "1h", "4h"} {
		m := fr[tf].(map[string]interface{})
		m["change_pct"] = 0.5
		m["oi_change_pct"] = 1.2
		m["cvd_delta"] = 20.0
	}
	b := ScoreLong(fr, map[string]interface{}{"direction": "long"})
	if len(b.Components) != 6 || b.Components[5].Name != "order_flow" {
		t.Fatalf("components = %+v", b.Components)
	}
	if b.Components[5].Score < 80 {
		t.Errorf("confirming flow scored %v", b.Components[5].Score)
	}
}

func TestRangeRegimeFlowPicksDirection(t *testing.T) {
	tf := frames("range", "range", "range", 1)
	for _, name := range []string{"15m", "1h", "4h"} {
		m := tf[name].(map[string]interface{})
		m["change_pct"] = -0.8
		m["oi_change_pct"] = 1.5
		m["cvd_delta"] = -30.0
	}
	r := AnalyzeSymbol("BTCUSDT", "spot", false, tf, nil, nil)
	if r.Direction != "short" {
		t.Errorf("range + new shorts should tie-break to short, got %q", r.Direction)
	}
}

func TestTrendNotOverriddenByFlow(t *testing.T) {
	tf := frames("bullish", "bullish", "bullish", 1)
	for _, name := range []string{"15m", "1h", "4h"} {
		m := tf[name].(map[string]interface{})
		m["change_pct"] = -0.8
		m["oi_change_pct"] = 1.5
		m["cvd_delta"] = -30.0
	}
	r := AnalyzeSymbol("BTCUSDT", "spot", false, tf, nil, nil)
	if r.Direction != "long" {
		t.Errorf("trend_up must stay long, flow only scores; got %q", r.Direction)
	}
}
