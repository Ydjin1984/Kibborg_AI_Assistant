package main

import "testing"

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
