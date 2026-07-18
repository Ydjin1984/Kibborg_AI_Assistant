package trading

import "testing"

func TestBuildPlanLong(t *testing.T) {
	p, ok := BuildPlan("long", 100, 1)
	if !ok {
		t.Fatal("long plan should build from price+atr")
	}
	if p.Entry != 100 {
		t.Errorf("entry = %v, want 100", p.Entry)
	}
	// Stop is below entry for a long; targets above; DCA between entry and stop.
	if p.Stop >= p.Entry {
		t.Errorf("long stop %v must be below entry %v", p.Stop, p.Entry)
	}
	for _, tp := range p.TakeProfit {
		if tp <= p.Entry {
			t.Errorf("long take-profit %v must be above entry %v", tp, p.Entry)
		}
	}
	for _, d := range p.Averaging {
		if d > p.Entry || d < p.Stop {
			t.Errorf("long DCA %v must sit between stop %v and entry %v", d, p.Stop, p.Entry)
		}
	}
	// Targets must be ordered increasingly (further = better R/R).
	if !(p.TakeProfit[0] < p.TakeProfit[1] && p.TakeProfit[1] < p.TakeProfit[2]) {
		t.Errorf("take-profits not increasing: %v", p.TakeProfit)
	}
}

func TestBuildPlanShort(t *testing.T) {
	p, ok := BuildPlan("short", 100, 1)
	if !ok {
		t.Fatal("short plan should build")
	}
	if p.Stop <= p.Entry {
		t.Errorf("short stop %v must be above entry %v", p.Stop, p.Entry)
	}
	for _, tp := range p.TakeProfit {
		if tp >= p.Entry {
			t.Errorf("short take-profit %v must be below entry %v", tp, p.Entry)
		}
	}
}

func TestBuildPlanNonDirectional(t *testing.T) {
	if _, ok := BuildPlan("wait/range", 100, 1); ok {
		t.Error("non-directional setup must not produce levels")
	}
	if _, ok := BuildPlan("long", 0, 1); ok {
		t.Error("zero price must not produce levels")
	}
	if _, ok := BuildPlan("long", 100, 0); ok {
		t.Error("zero ATR must not produce levels")
	}
}

func TestRoundPriceMagnitude(t *testing.T) {
	// Big caps keep 2 decimals, micro-caps keep significant digits.
	if got := roundPrice(61234.5678); got != 61234.57 {
		t.Errorf("roundPrice(big) = %v", got)
	}
	if got := roundPrice(0.000012345678); got != 0.00001235 {
		t.Errorf("roundPrice(micro) = %v", got)
	}
}
