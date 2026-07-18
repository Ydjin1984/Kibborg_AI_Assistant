package trading

// Deterministic trade-plan generator. This is the piece the Go port was missing: the Python
// stack received ready-made entry/stop/take-profit levels in its legacy_plan, but the Go
// engine only had candles. Here we derive concrete levels from price + ATR (Wilder ATR from
// indicators.go), so /analyze can output real entry / DCA / SL / TP / R:R — the "TP/SL,
// trailing, risk management" the product promises — with every number computed, not guessed.

import (
	"fmt"
	"math"
)

// Plan geometry, expressed in ATR multiples so it scales with each instrument's volatility.
const (
	planStopATR = 1.5 // stop sits 1.5·ATR from entry
	planTP1R    = 1.5 // take-profits as reward/risk multiples of the stop distance
	planTP2R    = 2.5
	planTP3R    = 4.0
	planDCA1R   = 0.4 // averaging levels strictly between entry and stop (never on the stop)
	planDCA2R   = 0.75
)

// TradePlan is a concrete, tradeable plan with levels the user can act on.
type TradePlan struct {
	Direction    string    `json:"direction"`
	Entry        float64   `json:"entry"`
	EntryZone    []float64 `json:"entry_zone"`
	Stop         float64   `json:"stop"`
	TakeProfit   []float64 `json:"take_profit"`
	Averaging    []float64 `json:"averaging"`
	RR           float64   `json:"rr"`
	HasRR        bool      `json:"has_rr"`
	Invalidation string    `json:"invalidation"`
}

// BuildPlan derives entry/DCA/SL/TP for a directional setup from the current price and ATR.
// It returns ok=false for a non-directional call or missing/degenerate inputs (price/ATR ≤ 0),
// so the caller emits a "wait" plan instead of inventing levels.
func BuildPlan(direction string, price, atr float64) (TradePlan, bool) {
	if (direction != "long" && direction != "short") || price <= 0 || atr <= 0 {
		return TradePlan{Direction: direction}, false
	}
	risk := planStopATR * atr
	var stop float64
	var tps, dca []float64
	if direction == "long" {
		stop = price - risk
		tps = []float64{price + planTP1R*risk, price + planTP2R*risk, price + planTP3R*risk}
		dca = []float64{price - planDCA1R*risk, price - planDCA2R*risk}
	} else {
		stop = price + risk
		tps = []float64{price - planTP1R*risk, price - planTP2R*risk, price - planTP3R*risk}
		dca = []float64{price + planDCA1R*risk, price + planDCA2R*risk}
	}
	return TradePlan{
		Direction:    direction,
		Entry:        roundPrice(price),
		EntryZone:    []float64{roundPrice(price)},
		Stop:         roundPrice(stop),
		TakeProfit:   roundPriceSlice(tps),
		Averaging:    roundPriceSlice(dca),
		Invalidation: invalidation(direction, roundPrice(stop)),
	}, true
}

func invalidation(direction string, stop float64) string {
	switch direction {
	case "long":
		return fmt.Sprintf("Лонг отменяется при закреплении ниже стопа %g.", stop)
	case "short":
		return fmt.Sprintf("Шорт отменяется при закреплении выше стопа %g.", stop)
	default:
		return "Нет сделки до подтверждения пробоя/ретеста."
	}
}

// roundPrice rounds to a sensible number of decimals for the price magnitude, so BTC
// (~60000) keeps 2 decimals while a micro-cap alt (~0.0000123) keeps its significant digits.
func roundPrice(p float64) float64 {
	if p == 0 {
		return 0
	}
	ap := math.Abs(p)
	digits := 8
	switch {
	case ap >= 1000:
		digits = 2
	case ap >= 1:
		digits = 4
	case ap >= 0.01:
		digits = 6
	}
	f := math.Pow(10, float64(digits))
	return math.Round(p*f) / f
}

func roundPriceSlice(xs []float64) []float64 {
	out := make([]float64, len(xs))
	for i, x := range xs {
		out[i] = roundPrice(x)
	}
	return out
}

// toMap renders the plan for the DecisionReport's Plan field (generic map for wire
// compatibility with the existing report shape).
func (p TradePlan) toMap() map[string]interface{} {
	m := map[string]interface{}{
		"direction":    p.Direction,
		"entry":        p.Entry,
		"entry_zone":   p.EntryZone,
		"stop":         p.Stop,
		"take_profit":  p.TakeProfit,
		"averaging":    p.Averaging,
		"invalidation": p.Invalidation,
	}
	if p.HasRR {
		m["rr"] = p.RR
	}
	return m
}
