package trading

import (
	"time"
)

// AnalyzeSymbol is the core deterministic aggregator.
// This is the Go equivalent of Python computer-tools/trading/brain.py + scoring + regime + etc.
// All quantitative output (scores, regime, confidence, plan, risk) MUST come from here or submodules.
// The LLM only narrates / routes.

func AnalyzeSymbol(symbol, market string, includeNews bool, timeframes map[string]interface{}, extraContexts map[string]interface{}, enabledModules []string) DecisionReport {
	now := time.Now().UTC()

	if len(enabledModules) == 0 {
		enabledModules = []string{"regime_classifier", "scoring"}
	}

	used := []string{}

	// 1. Regime (ported logic) - only if selected
	var regimeRes RegimeResult
	if containsString(enabledModules, "regime_classifier") {
		regimeRes = ClassifyRegime(timeframes, extraContexts)
		used = append(used, "regime_classifier")
	} else {
		regimeRes = RegimeResult{Status: "DISABLED", Regime: "user_disabled"}
	}

	// 2. Direction from the regime. Only clear trends (and panic → short) take a side;
	//    ambiguous regimes (range, squeeze, volatile, transition, unknown) stay neutral so
	//    the engine never recommends a long just because it couldn't classify the market.
	dir := "wait/range"
	switch regimeRes.Regime {
	case "trend_up":
		dir = "long"
	case "trend_down", "panic":
		dir = "short"
	}

	// 3. Scoring (ported) - only if selected
	var breakdown ScoreBreakdown
	if containsString(enabledModules, "scoring") {
		switch dir {
		case "long":
			breakdown = ScoreLong(timeframes, map[string]interface{}{"direction": dir})
		case "short":
			breakdown = ScoreShort(timeframes, map[string]interface{}{"direction": dir})
		default:
			breakdown = ScoreBreakdown{
				Direction:  "wait/range",
				FinalScore: 35,
				Components: []ScoreComponent{},
				Regime:     regimeRes.Regime,
			}
		}
		used = append(used, "scoring")
	} else {
		breakdown = ScoreBreakdown{Direction: dir, FinalScore: 50}
	}

	// 4. Trade plan + risk. Derive concrete entry/DCA/SL/TP from 1h price + ATR, then assess
	//    the geometry (R:R, stop distance). Non-directional setups yield a neutral "wait" plan.
	price, atr := planInputs(timeframes)
	plan, planOK := BuildPlan(breakdown.Direction, price, atr)
	risk := AssessTradePlan(breakdown.Direction, plan.EntryZone, plan.Stop, planOK, plan.TakeProfit, defaultMinRR)
	if planOK {
		plan.RR, plan.HasRR = risk.RR, risk.HasRR
	}

	// 5. Decision gate (ported from build_decision_report): the final verdict combines score
	//    and risk, and a warning-heavy regime downgrades a would-be ALLOW to WATCH.
	decision := decideVerdict(breakdown.Direction, breakdown.FinalScore, risk.Status)
	if decision == "ALLOW" && regimeHasWarning(regimeRes) {
		decision = "WATCH"
	}

	// 6. Confidence: keep the report's numeric confidence/probability in the calibrated bounds
	//    the callers expect, and attach the full calibration context for anyone who wants the
	//    breakdown (source of truth for "how sure" — read the calibration, not the prose).
	conf := regimeRes.Confidence * 0.7
	if breakdown.FinalScore > 70 {
		conf += 0.15
	}
	conf = clamp(conf, 0.05, 0.92)
	calibration := CalibrateConfidence(ConfidenceInputs{
		Score:            breakdown.FinalScore,
		RegimeConfidence: regimeRes.Confidence,
		OrderflowConfirm: orderflowConfirm(extraContexts),
		WarningFlags:     countSeverity(regimeRes.Flags, "warning"),
		BlockerFlags:     countSeverity(regimeRes.Flags, "blocker"),
		DisciplineStatus: "ALLOW", // no journal in the Go engine yet → discipline is neutral
		Decision:         decision,
	})

	// 7. Build the canonical DecisionReport (the single source of truth)
	report := DecisionReport{
		Symbol:      symbol,
		Market:      market,
		Timestamp:   now,
		Direction:   breakdown.Direction,
		Score:       breakdown.FinalScore,
		FinalScore:  breakdown.FinalScore,
		Regime:      regimeRes.Regime,
		Strategy:    "regime_" + regimeRes.Regime,
		Confidence:  mathRound(conf, 3),
		Probability: clamp(conf*0.9+0.05, 0.1, 0.95),
		Risk:        risk.toMap(),
		Plan:        plan.toMap(),
		DisciplineGuard: map[string]interface{}{
			"status": "ALLOW",
			"note":   "Дисциплинарный гард нейтрален: журнал сделок в Go-движке пока не ведётся.",
		},
		DataQuality: map[string]interface{}{
			"status": dataQualityStatus(timeframes),
		},
		ContextFlags: []map[string]interface{}{
			{"severity": "info", "code": "GO_ENGINE", "message": "Рассчитано движком Kibborg-Go"},
		},
		Meta: map[string]interface{}{
			"engine":                 "kibborg-go",
			"version":                "0.4.0",
			"decision":               decision,
			"regime":                 regimeRes,
			"scoring":                breakdown,
			"confidence_calibration": calibration,
			"contexts":               extraContexts,
			"modules_requested":      enabledModules,
			"modules_used":           used,
			"invariant":              "LLM never invents numbers - all values from this Go module",
		},
	}

	return report
}

// planInputs pulls the price and ATR the plan generator needs, preferring the 1h frame
// (the plan's reference timeframe) and falling back to 15m.
func planInputs(timeframes map[string]interface{}) (price, atr float64) {
	for _, name := range []string{"1h", "15m", "4h"} {
		if tf, ok := timeframes[name].(map[string]interface{}); ok {
			p := _num(tf["close"], 0)
			a := _num(tf["atr14"], 0)
			if p > 0 && a > 0 {
				return p, a
			}
			if price == 0 && p > 0 {
				price = p
			}
		}
	}
	return price, atr
}

// decideVerdict is the ALLOW/WATCH/WAIT/REJECT gate from build_decision_report.
func decideVerdict(direction string, finalScore float64, riskStatus string) string {
	if direction != "long" && direction != "short" {
		return "WAIT"
	}
	switch {
	case riskStatus == "reject":
		return "REJECT"
	case finalScore >= 70 && (riskStatus == "ok" || riskStatus == "warning"):
		return "ALLOW"
	case finalScore >= 55:
		return "WATCH"
	default:
		return "WAIT"
	}
}

func regimeHasWarning(r RegimeResult) bool {
	for _, f := range r.Flags {
		if f["severity"] == "warning" {
			return true
		}
	}
	return false
}

func countSeverity(flags []map[string]string, severity string) int {
	n := 0
	for _, f := range flags {
		if f["severity"] == severity {
			n++
		}
	}
	return n
}

// orderflowConfirm maps the order-flow bias context into the supports/against language the
// confidence calibration expects (best-effort; empty when there is no order-flow context).
func orderflowConfirm(extra map[string]interface{}) string {
	if extra == nil {
		return ""
	}
	of, ok := extra["orderflow"].(map[string]interface{})
	if !ok {
		return ""
	}
	if c, ok := of["confirmation"].(string); ok {
		return c
	}
	return ""
}

// dataQualityStatus reports "ok" when every core frame carries the fields the engine reads,
// else "partial" — so a thin data set is never presented as a full-confidence answer.
func dataQualityStatus(timeframes map[string]interface{}) string {
	for _, name := range []string{"15m", "1h", "4h"} {
		tf, ok := timeframes[name].(map[string]interface{})
		if !ok {
			return "partial"
		}
		for _, key := range []string{"close", "atr14", "rsi14", "trend"} {
			if _, has := tf[key]; !has {
				return "partial"
			}
		}
	}
	return "ok"
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
