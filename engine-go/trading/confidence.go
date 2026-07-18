package trading

// Confidence calibration. Port of the core of computer-tools/trading/confidence.py: it turns
// the raw setup score into a grounded confidence, then nudges it by the factors the Go engine
// actually has (regime confidence, order-flow confirmation, context warnings, discipline,
// final decision). The result is a structured context (score + label + factors + penalties),
// mirroring confidence_calibration_v1 — read this, not prose, for how sure the engine is.

import "math"

// ConfidenceFactor / penalty entries explain how the final number was reached.
type confAdjust struct {
	Name   string  `json:"name"`
	Impact float64 `json:"impact"`
}

// ConfidenceInputs are the already-computed signals CalibrateConfidence weighs. Keeping them
// explicit (instead of re-reading a loose map) keeps the calibration a pure function.
type ConfidenceInputs struct {
	Score            float64 // final setup score, 0..100
	RegimeConfidence float64 // 0..1
	OrderflowConfirm string  // supports | against | ""
	WarningFlags     int
	BlockerFlags     int
	DisciplineStatus string // ALLOW | WAIT | REJECT
	Decision         string // ALLOW | WATCH | WAIT | REJECT
}

func confLabel(score float64) string {
	switch {
	case score >= 0.75:
		return "high"
	case score >= 0.55:
		return "medium"
	case score >= 0.35:
		return "low"
	default:
		return "very_low"
	}
}

// CalibrateConfidence returns the confidence calibration context (v1-compatible shape).
func CalibrateConfidence(in ConfidenceInputs) map[string]interface{} {
	base := clamp(in.Score/100.0, 0.0, 1.0)
	conf := math.Max(0.05, math.Min(0.95, base))
	factors := []confAdjust{{Name: "base_decision_score", Impact: mathRound(conf, 3)}}
	var penalties []confAdjust

	if in.RegimeConfidence > 0 {
		impact := (in.RegimeConfidence - 0.6) * 0.12
		conf += impact
		factors = append(factors, confAdjust{"regime_confidence", mathRound(impact, 3)})
	}
	switch in.OrderflowConfirm {
	case "supports":
		conf += 0.04
		factors = append(factors, confAdjust{"orderflow_supports", 0.04})
	case "against":
		conf -= 0.1
		penalties = append(penalties, confAdjust{"orderflow_against", -0.1})
	}
	if in.WarningFlags > 0 {
		impact := -math.Min(0.2, float64(in.WarningFlags)*0.04)
		conf += impact
		penalties = append(penalties, confAdjust{"context_warnings", mathRound(impact, 3)})
	}
	if in.BlockerFlags > 0 {
		impact := -math.Min(0.5, float64(in.BlockerFlags)*0.18)
		conf += impact
		penalties = append(penalties, confAdjust{"context_blockers", mathRound(impact, 3)})
	}
	switch in.DisciplineStatus {
	case "WAIT":
		conf -= 0.16
		penalties = append(penalties, confAdjust{"discipline_wait", -0.16})
	case "REJECT":
		conf -= 0.35
		penalties = append(penalties, confAdjust{"discipline_reject", -0.35})
	}
	switch in.Decision {
	case "WAIT":
		conf = math.Min(conf, 0.55)
	case "REJECT":
		conf = math.Min(conf, 0.25)
	}

	calibrated := math.Max(0.01, math.Min(0.95, conf))
	var flags []map[string]string
	if calibrated < 0.35 {
		flags = append(flags, map[string]string{
			"code": "LOW_CALIBRATED_CONFIDENCE", "severity": "warning", "source": "confidence",
			"message": "Калиброванная уверенность низкая — не подавай это как сильный сетап.",
		})
	}
	return map[string]interface{}{
		"version":    "confidence_calibration_v1",
		"status":     "OK",
		"score":      mathRound(calibrated, 3),
		"label":      confLabel(calibrated),
		"base_score": mathRound(base, 3),
		"factors":    factors,
		"penalties":  penalties,
		"flags":      flags,
	}
}
