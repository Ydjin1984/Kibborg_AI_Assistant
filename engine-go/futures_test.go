package main

import "testing"

func TestFundingBarIndex(t *testing.T) {
	const step = 15 * 60 * 1000
	opens := []int64{1_000_000, 1_000_000 + step, 1_000_000 + 2*step}
	cases := []struct {
		t    int64
		want int
	}{
		{1_000_000, 0},
		{1_000_000 + step - 1, 0},
		{1_000_000 + step, 1},
		{1_000_000 + 3*step, -1},
		{999_999, -1},
	}
	for _, c := range cases {
		if got := fundingBarIndex(opens, c.t, step); got != c.want {
			t.Errorf("fundingBarIndex(%d) = %d, want %d", c.t, got, c.want)
		}
	}
	if fundingBarIndex(nil, 1, step) != -1 {
		t.Error("empty opens must be -1")
	}
}

func TestMapFundingToBarsSkipsOutsideAndWrongTF(t *testing.T) {
	const step = 5 * 60 * 1000
	opens := []int64{10_000, 10_000 + step, 10_000 + 2*step}
	ev := []fundingEvent{
		{Time: 10_000 + 100, Rate: 0.0001},
		{Time: 10_000 + 3*step + 50, Rate: -0.0002}, // после закрытия последней свечи
	}
	got := mapFundingToBars(ev, opens, step)
	if len(got) != 1 || got[0].Index != 0 || got[0].Rate != 0.0001 {
		t.Fatalf("got %+v", got)
	}
	if mapFundingToBars(ev, opens, 0) != nil {
		t.Error("1h/4h/1d must not invent funding marks")
	}
	if chartFundingInterval("1h") != 0 || chartFundingInterval("1d") != 0 {
		t.Error("funding marks only on 5m/15m/30m")
	}
	if chartFundingInterval("5m") != step || chartFundingInterval("30m") != 30*60*1000 {
		t.Error("interval millis mismatch")
	}
}

func TestBuildLevelAndCVDCandles(t *testing.T) {
	const step = 15 * 60 * 1000
	opens := []int64{1000, 1000 + step, 1000 + 2*step}
	oi := []timedValue{
		{T: 1000 + 10, V: 100},
		{T: 1000 + step + 10, V: 110},
		{T: 1000 + 2*step + 10, V: 105},
	}
	bars := buildLevelCandles(oi, opens, step)
	if len(bars) != 3 {
		t.Fatalf("oi bars = %d", len(bars))
	}
	if bars[1].O != 100 || bars[1].C != 110 || bars[1].H != 110 || bars[1].L != 100 {
		t.Errorf("second OI candle = %+v", bars[1])
	}
	deltas := []timedValue{
		{T: 1000 + 10, V: 5},
		{T: 1000 + step + 10, V: -2},
	}
	cvd := buildCVDCandles(deltas, opens, step)
	if len(cvd) != 2 {
		t.Fatalf("cvd bars = %d", len(cvd))
	}
	if cvd[0].O != 0 || cvd[0].C != 5 || cvd[1].C != 3 || cvd[1].D != -2 {
		t.Errorf("cvd = %+v", cvd)
	}
}

func TestOrderflowBiasFromRatio(t *testing.T) {
	cases := map[float64]string{
		1.50: "buy_pressure",
		1.15: "buy_pressure",
		1.00: "neutral",
		0.90: "neutral",
		0.87: "sell_pressure",
		0.50: "sell_pressure",
		0.00: "neutral", // guard: a zero/garbage ratio is not "sell_pressure"
	}
	for ratio, want := range cases {
		if got := orderflowBiasFromRatio(ratio); got != want {
			t.Errorf("orderflowBiasFromRatio(%.2f) = %q, want %q", ratio, got, want)
		}
	}
}
