package trading

import (
	"math"
	"testing"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestSizePosition_LongBasic(t *testing.T) {
	s := SizePosition(SizingInput{
		Direction: "long", Entry: 100, Stop: 95, Balance: 1000, RiskPct: 2, Leverage: 10,
		Targets: []float64{115}, MaintMargin: 0.005,
	})
	if !s.OK {
		t.Fatalf("ожидался OK, ошибка: %s", s.Err)
	}
	if !approx(s.RiskAmount, 20) {
		t.Errorf("riskAmount=%.4f, ожидалось 20", s.RiskAmount)
	}
	if !approx(s.Qty, 4) { // 20 / (100-95)
		t.Errorf("qty=%.4f, ожидалось 4", s.Qty)
	}
	if !approx(s.Notional, 400) {
		t.Errorf("notional=%.4f, ожидалось 400", s.Notional)
	}
	if !approx(s.Margin, 40) { // 400 / 10
		t.Errorf("margin=%.4f, ожидалось 40", s.Margin)
	}
	if !approx(s.RR, 3) { // reward 15 / risk 5
		t.Errorf("rr=%.4f, ожидалось 3", s.RR)
	}
	// liq long @lev10 = 100*(1-0.1+0.005)=90.5, below stop 95 → safe
	if s.LiqBeforeStop {
		t.Errorf("liqBeforeStop не ожидался: liq=%.4f stop=95", s.LiquidationPrice)
	}
	if !approx(s.LiquidationPrice, 90.5) {
		t.Errorf("liq=%.4f, ожидалось 90.5", s.LiquidationPrice)
	}
}

func TestSizePosition_LiquidationBeforeStop(t *testing.T) {
	// lev 20: liq = 100*(1-0.05+0.005)=95.5, which is ABOVE the stop 95 → liquidated first.
	s := SizePosition(SizingInput{
		Direction: "long", Entry: 100, Stop: 95, Balance: 1000, RiskPct: 2, Leverage: 20, MaintMargin: 0.005,
	})
	if !s.OK {
		t.Fatal(s.Err)
	}
	if !s.LiqBeforeStop {
		t.Errorf("ожидался liqBeforeStop: liq=%.4f stop=95", s.LiquidationPrice)
	}
	found := false
	for _, w := range s.Warnings {
		if len(w) > 0 {
			found = true
		}
	}
	if !found {
		t.Error("ожидалось предупреждение о ликвидации")
	}
}

func TestSizePosition_ShortAndSpot(t *testing.T) {
	// Short, no leverage (spot) → no liquidation price.
	s := SizePosition(SizingInput{
		Direction: "short", Entry: 200, Stop: 210, Balance: 5000, RiskPct: 1, Leverage: 1, MaintMargin: 0.005,
	})
	if !s.OK {
		t.Fatal(s.Err)
	}
	if !approx(s.RiskAmount, 50) { // 5000 * 1%
		t.Errorf("riskAmount=%.4f, ожидалось 50", s.RiskAmount)
	}
	if !approx(s.Qty, 5) { // 50 / (210-200)
		t.Errorf("qty=%.4f, ожидалось 5", s.Qty)
	}
	if s.LiquidationPrice != 0 {
		t.Errorf("на споте ликвидации быть не должно, got %.4f", s.LiquidationPrice)
	}
}

func TestSizePosition_RejectsBadInput(t *testing.T) {
	cases := []SizingInput{
		{Direction: "long", Entry: 100, Stop: 105, Balance: 1000, RiskPct: 1},    // stop wrong side for long
		{Direction: "short", Entry: 100, Stop: 95, Balance: 1000, RiskPct: 1},    // stop wrong side for short
		{Direction: "long", Entry: 100, Stop: 95, Balance: 0, RiskPct: 1},        // no balance
		{Direction: "long", Entry: 100, Stop: 95, Balance: 1000, RiskPct: 0},     // no risk
		{Direction: "sideways", Entry: 100, Stop: 95, Balance: 1000, RiskPct: 1}, // bad direction
	}
	for i, in := range cases {
		if s := SizePosition(in); s.OK {
			t.Errorf("случай %d: ожидался отказ, но OK", i)
		}
	}
}
