package trading

// Deterministic risk assessment of a trade plan. Direct port of the Python reference
// computer-tools/trading/risk.py (assess_trade_plan). Given a direction + entry/stop/take-
// profit levels it validates the geometry (stop on the correct side, targets on the profit
// side), computes reward/risk and the stop distance, and returns ok / warning / reject with
// human-readable flags. No network, no state — pure math, so the "engine never invents
// numbers" invariant holds: every value here is derived from the levels passed in.

import (
	"fmt"
	"math"
	"strings"
)

// RiskFlag is a non-fatal caution about a trade plan.
type RiskFlag struct {
	Code     string `json:"code"`
	Severity string `json:"severity"` // info | warning
	Message  string `json:"message"`
}

// RejectReason is a fatal reason a plan is not tradeable.
type RejectReason struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// RiskAssessment is the verdict on one plan's geometry.
type RiskAssessment struct {
	Status          string         `json:"status"` // ok | warning | reject | wait
	RR              float64        `json:"rr"`     // reward/risk to the best valid target (0 = n/a)
	HasRR           bool           `json:"has_rr"`
	StopDistancePct float64        `json:"stop_distance_pct"`
	Flags           []RiskFlag     `json:"flags"`
	Rejects         []RejectReason `json:"reject_reasons"`
}

// defaultMinRR mirrors risk.py's min_rr default.
const defaultMinRR = 1.5

func entryMidpoint(zone []float64) (float64, bool) {
	if len(zone) == 0 {
		return 0, false
	}
	sum := 0.0
	for _, v := range zone {
		sum += v
	}
	return sum / float64(len(zone)), true
}

// riskReward returns reward/risk for a target, and whether it is valid for the direction.
func riskReward(direction string, entry, stop, target float64, hasTarget bool) (float64, bool) {
	if !hasTarget {
		return 0, false
	}
	risk := math.Abs(entry - stop)
	if risk <= 0 {
		return 0, false
	}
	if direction == "long" && (stop >= entry || target <= entry) {
		return 0, false
	}
	if direction == "short" && (stop <= entry || target >= entry) {
		return 0, false
	}
	return math.Abs(target-entry) / risk, true
}

// firstValidTakeProfit returns the nearest target on the profit side.
func firstValidTakeProfit(direction string, entry float64, targets []float64) (float64, bool) {
	for _, tp := range targets {
		if direction == "long" && tp > entry {
			return tp, true
		}
		if direction == "short" && tp < entry {
			return tp, true
		}
	}
	return 0, false
}

// bestValidTakeProfit returns the furthest target on the profit side (best R/R).
func bestValidTakeProfit(direction string, entry float64, targets []float64) (float64, bool) {
	var best float64
	found := false
	for _, tp := range targets {
		if direction == "long" && tp > entry {
			if !found || tp > best {
				best, found = tp, true
			}
		}
		if direction == "short" && tp < entry {
			if !found || tp < best {
				best, found = tp, true
			}
		}
	}
	return best, found
}

// AssessTradePlan validates a plan's geometry and returns the risk verdict. entryOK/stopOK
// report whether an entry/stop was actually supplied (mirrors the Python None checks).
func AssessTradePlan(direction string, entryZone []float64, stop float64, stopOK bool, takeProfit []float64, minRR float64) RiskAssessment {
	if minRR <= 0 {
		minRR = defaultMinRR
	}
	direction = strings.ToLower(strings.TrimSpace(direction))
	var flags []RiskFlag
	var rejects []RejectReason

	if direction != "long" && direction != "short" {
		flags = append(flags, RiskFlag{"no_directional_setup", "info", "Нет направленного сетапа — ждём подтверждения."})
		return RiskAssessment{Status: "wait", Flags: flags}
	}

	entry, entryOK := entryMidpoint(entryZone)
	if !entryOK {
		rejects = append(rejects, RejectReason{"missing_entry", "Зона входа отсутствует или некорректна."})
	}
	if !stopOK {
		rejects = append(rejects, RejectReason{"missing_stop", "Стоп отсутствует или некорректен."})
	}
	if len(takeProfit) == 0 {
		rejects = append(rejects, RejectReason{"missing_take_profit", "Тейк-профиты отсутствуют или некорректны."})
	}
	if len(rejects) > 0 {
		return RiskAssessment{Status: "reject", Flags: flags, Rejects: rejects}
	}

	if direction == "long" && stop >= entry {
		rejects = append(rejects, RejectReason{"invalid_stop_side", "Для лонга стоп должен быть ниже входа."})
	}
	if direction == "short" && stop <= entry {
		rejects = append(rejects, RejectReason{"invalid_stop_side", "Для шорта стоп должен быть выше входа."})
	}

	firstTarget, firstOK := firstValidTakeProfit(direction, entry, takeProfit)
	bestTarget, bestOK := bestValidTakeProfit(direction, entry, takeProfit)
	if !bestOK {
		rejects = append(rejects, RejectReason{"invalid_take_profit_side", "Ни один тейк не на стороне прибыли."})
	}

	rr, hasRR := riskReward(direction, entry, stop, bestTarget, bestOK)
	firstRR, hasFirstRR := riskReward(direction, entry, stop, firstTarget, firstOK)
	stopDistancePct := 0.0
	if entry != 0 {
		stopDistancePct = math.Abs(entry-stop) / entry * 100
	}
	if hasRR && rr < minRR {
		rejects = append(rejects, RejectReason{"low_rr", fmt.Sprintf("R/R %.2f ниже минимума %.2f.", rr, minRR)})
	} else if hasFirstRR && firstRR < minRR {
		flags = append(flags, RiskFlag{"low_first_target_rr", "warning", fmt.Sprintf("R/R до первого тейка %.2f ниже %.2f — фиксируй частями осторожно.", firstRR, minRR)})
	}
	if stopDistancePct > 0 && stopDistancePct < 0.15 {
		flags = append(flags, RiskFlag{"tight_stop", "warning", "Стоп очень узкий — спред/проскальзывание могут его выбить."})
	}
	if stopDistancePct > 6 {
		flags = append(flags, RiskFlag{"wide_stop", "warning", "Стоп широкий — уменьши размер или жди лучшего входа."})
	}

	status := "ok"
	if len(rejects) > 0 {
		status = "reject"
	} else {
		for _, f := range flags {
			if f.Severity == "warning" {
				status = "warning"
				break
			}
		}
	}
	return RiskAssessment{
		Status:          status,
		RR:              mathRound(rr, 2),
		HasRR:           hasRR,
		StopDistancePct: mathRound(stopDistancePct, 3),
		Flags:           flags,
		Rejects:         rejects,
	}
}

// toMap renders the assessment for the DecisionReport's Risk field (kept as a generic map
// for wire compatibility with the existing report shape).
func (r RiskAssessment) toMap() map[string]interface{} {
	flags := make([]map[string]string, 0, len(r.Flags))
	for _, f := range r.Flags {
		flags = append(flags, map[string]string{"code": f.Code, "severity": f.Severity, "message": f.Message})
	}
	rejects := make([]map[string]string, 0, len(r.Rejects))
	for _, rj := range r.Rejects {
		rejects = append(rejects, map[string]string{"code": rj.Code, "message": rj.Message})
	}
	m := map[string]interface{}{
		"status":            r.Status,
		"stop_distance_pct": r.StopDistancePct,
		"flags":             flags,
		"reject_reasons":    rejects,
	}
	if r.HasRR {
		m["rr"] = r.RR
	}
	return m
}
