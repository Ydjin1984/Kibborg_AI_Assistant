package trading

// Deterministic position sizing: given an account balance, a per-trade risk %, and the
// entry/stop of a setup, compute HOW MUCH to buy so a stop-out loses exactly the intended
// risk — plus notional, required margin at a chosen leverage, and an isolated-margin
// liquidation estimate. This is the risk-management layer the audit flagged as missing
// (risk.go only validated level geometry). Pure math, no network, no state — so the "engine
// never invents numbers" invariant holds: every value is derived from the inputs.

import (
	"fmt"
	"math"
	"strings"
)

// SizingInput describes the account and the setup to size.
type SizingInput struct {
	Direction   string    // long | short
	Entry       float64   // planned entry price
	Stop        float64   // stop-loss price
	Balance     float64   // account equity (same currency as price quote, e.g. USDT)
	RiskPct     float64   // percent of balance to risk on this trade, e.g. 1.5 = 1.5%
	Leverage    float64   // ≥1; ≤1 means spot / no leverage (no liquidation)
	Targets     []float64 // optional take-profits (for R:R)
	MaintMargin float64   // maintenance margin rate for the liquidation estimate (e.g. 0.005)
}

// Sizing is the computed position size and risk figures.
type Sizing struct {
	OK               bool     `json:"ok"`
	Err              string   `json:"err,omitempty"`
	Direction        string   `json:"direction"`
	Entry            float64  `json:"entry"`
	Stop             float64  `json:"stop"`
	Balance          float64  `json:"balance"`
	RiskPct          float64  `json:"risk_pct"`
	RiskAmount       float64  `json:"risk_amount"`       // balance * riskPct/100 — max loss if stopped
	StopDistance     float64  `json:"stop_distance"`     // |entry - stop|
	StopDistancePct  float64  `json:"stop_distance_pct"` // relative to entry
	Qty              float64  `json:"qty"`               // position size in base units
	Notional         float64  `json:"notional"`          // qty * entry
	Leverage         float64  `json:"leverage"`
	Margin           float64  `json:"margin"`            // notional / leverage
	LiquidationPrice float64  `json:"liquidation_price"` // 0 = n/a (spot / no leverage)
	LiqBeforeStop    bool     `json:"liq_before_stop"`   // liquidation would trigger before the stop
	RR               float64  `json:"rr"`                // to furthest valid target (0 = n/a)
	Warnings         []string `json:"warnings,omitempty"`
}

// SizePosition computes the position size and risk figures for a setup. It never guesses:
// invalid inputs return OK=false with a reason instead of a fabricated size.
func SizePosition(in SizingInput) Sizing {
	dir := strings.ToLower(strings.TrimSpace(in.Direction))
	out := Sizing{Direction: dir, Entry: in.Entry, Stop: in.Stop, Balance: in.Balance, RiskPct: in.RiskPct}

	if dir != "long" && dir != "short" {
		out.Err = "направление должно быть long или short"
		return out
	}
	if in.Entry <= 0 || in.Stop <= 0 {
		out.Err = "нужны положительные цены входа и стопа"
		return out
	}
	if in.Balance <= 0 {
		out.Err = "нужен положительный баланс счёта"
		return out
	}
	if in.RiskPct <= 0 {
		out.Err = "риск на сделку должен быть > 0%"
		return out
	}
	if dir == "long" && in.Stop >= in.Entry {
		out.Err = "для лонга стоп должен быть ниже входа"
		return out
	}
	if dir == "short" && in.Stop <= in.Entry {
		out.Err = "для шорта стоп должен быть выше входа"
		return out
	}

	lev := in.Leverage
	if lev < 1 {
		lev = 1
	}
	mm := in.MaintMargin
	if mm <= 0 {
		mm = 0.005
	}

	stopDist := math.Abs(in.Entry - in.Stop)
	riskAmount := in.Balance * in.RiskPct / 100.0
	qty := riskAmount / stopDist
	notional := qty * in.Entry
	margin := notional / lev

	out.OK = true
	out.Leverage = lev
	out.RiskAmount = mathRound(riskAmount, 2)
	out.StopDistance = roundPrice(stopDist)
	out.StopDistancePct = mathRound(stopDist/in.Entry*100, 3)
	out.Qty = roundQty(qty)
	out.Notional = mathRound(notional, 2)
	out.Margin = mathRound(margin, 2)

	// Isolated-margin liquidation estimate (fees/funding ignored). Only meaningful with leverage.
	if lev > 1 {
		var liq float64
		if dir == "long" {
			liq = in.Entry * (1 - 1/lev + mm)
		} else {
			liq = in.Entry * (1 + 1/lev - mm)
		}
		if liq < 0 {
			liq = 0
		}
		out.LiquidationPrice = roundPrice(liq)
		// If liquidation sits between entry and stop, the position is wiped out BEFORE the stop
		// can act — the single most dangerous sizing mistake with leverage.
		if dir == "long" {
			out.LiqBeforeStop = liq >= in.Stop
		} else {
			out.LiqBeforeStop = liq <= in.Stop
		}
	}

	// R:R to the furthest valid target, if targets were supplied.
	if best, ok := bestValidTakeProfit(dir, in.Entry, in.Targets); ok {
		if rr, has := riskReward(dir, in.Entry, in.Stop, best, true); has {
			out.RR = mathRound(rr, 2)
		}
	}

	out.Warnings = sizingWarnings(out, lev)
	return out
}

func sizingWarnings(s Sizing, lev float64) []string {
	var w []string
	if s.LiqBeforeStop {
		w = append(w, fmt.Sprintf("🔴 Ликвидация (~%s) наступит РАНЬШЕ стопа при плече %g× — снизь плечо или отодвинь стоп.",
			numFmt(s.LiquidationPrice), lev))
	}
	if s.Margin > s.Balance {
		w = append(w, fmt.Sprintf("🔴 Требуемая маржа %s > баланса %s — позиция не откроется. Уменьши риск%% или повысь плечо.",
			numFmt(s.Margin), numFmt(s.Balance)))
	}
	if lev >= 25 {
		w = append(w, fmt.Sprintf("🟡 Очень высокое плечо %g× — малое движение против тебя близко к ликвидации.", lev))
	}
	if s.RiskPct > 5 {
		w = append(w, fmt.Sprintf("🟡 Риск %.1f%% на сделку — агрессивно; классический предел 1–2%%.", s.RiskPct))
	}
	if s.StopDistancePct > 0 && s.StopDistancePct < 0.15 {
		w = append(w, "🟡 Стоп очень узкий — спред/проскальзывание могут его выбить.")
	}
	if s.StopDistancePct > 10 {
		w = append(w, "🟡 Стоп широкий (>10%) — позиция получилась маленькой; жди лучшего входа или дроби риск.")
	}
	return w
}

// roundQty keeps a sensible number of significant digits for a base-asset quantity, which can
// range from fractions of a BTC to millions of a micro-cap token.
func roundQty(q float64) float64 {
	if q == 0 {
		return 0
	}
	aq := math.Abs(q)
	digits := 2
	switch {
	case aq >= 1000:
		digits = 2
	case aq >= 1:
		digits = 4
	case aq >= 0.01:
		digits = 6
	default:
		digits = 8
	}
	f := math.Pow(10, float64(digits))
	return math.Round(q*f) / f
}

// numFmt formats a float without trailing zeros (local helper to avoid importing strconv here).
func numFmt(f float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.8f", f), "0"), ".")
}
