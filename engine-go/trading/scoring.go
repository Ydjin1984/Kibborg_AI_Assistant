package trading

import (
	"math"
	"strconv"
)

// Note: ScoreComponent and ScoreBreakdown are defined in schema.go (reference truth).

type ScoreWeights struct {
	TrendAlignment float64
	Momentum       float64
	Volume         float64
	Structure      float64
	PlanQuality    float64
}

var DefaultWeights = ScoreWeights{
	TrendAlignment: 0.30,
	Momentum:       0.20,
	Volume:         0.12,
	Structure:      0.18,
	PlanQuality:    0.20,
}

// orderedTFWeights fixes the per-timeframe iteration order. The scores are order-independent
// sums, but the human-readable Reason strings must not shuffle between runs — determinism is
// this engine's whole point, so trend/structure scoring iterate this slice, not a map.
var orderedTFWeights = []struct {
	name string
	w    float64
}{
	{"15m", 0.25},
	{"1h", 0.35},
	{"4h", 0.40},
}

func _numF(v interface{}, def float64) float64 {
	if v == nil {
		return def
	}
	f, ok := v.(float64)
	if !ok {
		return def
	}
	return f
}

func _direction(plan map[string]interface{}) string {
	if plan == nil {
		return "wait/range"
	}
	d := ""
	if dir, ok := plan["direction"].(string); ok {
		d = dir
	}
	low := ""
	for _, r := range d {
		low += string(r | 0x20)
	}
	if low == "long" || low == "short" {
		return low
	}
	return "wait/range"
}

func ScoreLong(timeframes map[string]interface{}, legacyPlan map[string]interface{}) ScoreBreakdown {
	return scoreDirection(timeframes, legacyPlan, "long", DefaultWeights)
}

func ScoreShort(timeframes map[string]interface{}, legacyPlan map[string]interface{}) ScoreBreakdown {
	return scoreDirection(timeframes, legacyPlan, "short", DefaultWeights)
}

func scoreDirection(timeframes map[string]interface{}, plan map[string]interface{}, direction string, w ScoreWeights) ScoreBreakdown {
	dir := _direction(plan)
	if dir != direction {
		dir = direction
	}

	components := []ScoreComponent{}
	reasons := []string{}

	ta := trendAlignment(timeframes, dir, w)
	components = append(components, ta)

	mom := momentumScore(timeframes, dir, w)
	components = append(components, mom)

	vol := volumeScore(timeframes, w)
	components = append(components, vol)

	components = append(components, structureScore(timeframes, dir, w))
	components = append(components, planQualityScore(timeframes, dir, w))

	final := 0.0
	for _, c := range components {
		final += c.Score * c.Weight
	}
	final = clamp(final, 0, 100)
	final, components = blendFlowScore(final, components, timeframes, dir, w)

	reg := "transition"
	if dir == "long" {
		reg = "trend_up"
	} else if dir == "short" {
		reg = "trend_down"
	}

	return ScoreBreakdown{
		Direction:  dir,
		FinalScore: mathRound(final, 2),
		Components: components,
		DirectionScores: map[string]interface{}{
			"trend":    ta.Score,
			"momentum": mom.Score,
			"volume":   vol.Score,
		},
		Regime:  reg,
		Reasons: reasons,
	}
}

func trendAlignment(timeframes map[string]interface{}, direction string, w ScoreWeights) ScoreComponent {
	if direction != "long" && direction != "short" {
		return ScoreComponent{Name: "trend_alignment", Score: 35, Weight: w.TrendAlignment, Reason: "No active direction; trend score capped."}
	}
	expected := "bullish"
	if direction == "short" {
		expected = "bearish"
	}
	opposite := "bearish"
	if direction == "short" {
		opposite = "bullish"
	}

	weighted := 0.0
	labels := []string{}
	for _, tw := range orderedTFWeights {
		trend := "range"
		if tf, ok := timeframes[tw.name].(map[string]interface{}); ok {
			if t, ok := tf["trend"].(string); ok {
				trend = t
			}
		}
		labels = append(labels, tw.name+":"+trend)
		if trend == expected {
			weighted += 100 * tw.w
		} else if trend == opposite {
			weighted += 15 * tw.w
		} else {
			weighted += 55 * tw.w
		}
	}
	return ScoreComponent{Name: "trend_alignment", Score: weighted, Weight: w.TrendAlignment, Reason: join(labels, " / ")}
}

func momentumScore(timeframes map[string]interface{}, direction string, w ScoreWeights) ScoreComponent {
	if direction != "long" && direction != "short" {
		return ScoreComponent{Name: "momentum", Score: 40, Weight: w.Momentum, Reason: "No active direction; momentum is informational only."}
	}
	scores := []float64{}
	reasons := []string{}
	for _, name := range []string{"15m", "1h", "4h"} {
		tf := map[string]interface{}{}
		if v, ok := timeframes[name].(map[string]interface{}); ok {
			tf = v
		}
		rsi := _numF(tf["rsi14"], 50)
		hist := 0.0
		if macd, ok := tf["macd"].(map[string]interface{}); ok {
			hist = _numF(macd["histogram"], 0)
		}
		var rsiS, macdS float64
		if direction == "long" {
			if rsi >= 45 && rsi <= 68 {
				rsiS = 85
			} else if (rsi >= 40 && rsi < 45) || (rsi > 68 && rsi <= 74) {
				rsiS = 65
			} else {
				rsiS = 35
			}
			if hist > 0 {
				macdS = 75
			} else if hist == 0 {
				macdS = 45
			} else {
				macdS = 25
			}
		} else {
			if rsi >= 32 && rsi <= 55 {
				rsiS = 85
			} else if (rsi >= 26 && rsi < 32) || (rsi > 55 && rsi <= 60) {
				rsiS = 65
			} else {
				rsiS = 35
			}
			if hist < 0 {
				macdS = 75
			} else if hist == 0 {
				macdS = 45
			} else {
				macdS = 25
			}
		}
		s := (rsiS * 0.65) + (macdS * 0.35)
		scores = append(scores, s)
		reasons = append(reasons, name+":rsi="+formatF(rsi)+",macd_hist="+formatF(hist))
	}
	avg := 0.0
	for _, s := range scores {
		avg += s
	}
	avg /= float64(len(scores))
	return ScoreComponent{Name: "momentum", Score: avg, Weight: w.Momentum, Reason: join(reasons, " / ")}
}

// structureScore rates how clean the market structure is FOR the trade direction, from the
// per-timeframe swing labels (HH/HL vs LH/LL), penalising an overextended entry (price far
// from EMA20 in ATR units). Replaces the old fixed stub.
func structureScore(timeframes map[string]interface{}, direction string, w ScoreWeights) ScoreComponent {
	if direction != "long" && direction != "short" {
		return ScoreComponent{Name: "structure", Score: 40, Weight: w.Structure, Reason: "Нет активного направления."}
	}
	want, opp := "up", "down"
	if direction == "short" {
		want, opp = "down", "up"
	}
	score := 0.0
	labels := []string{}
	for _, tw := range orderedTFWeights {
		swing := "choppy"
		if tf, ok := timeframes[tw.name].(map[string]interface{}); ok {
			if s, ok := tf["swing"].(string); ok {
				swing = s
			}
		}
		s := 50.0
		if swing == want {
			s = 90
		} else if swing == opp {
			s = 20
		}
		score += s * tw.w
		labels = append(labels, tw.name+":"+swing)
	}
	// Overextension: a stretched price (far above/below EMA20) is a poor continuation entry.
	if tf, ok := timeframes["1h"].(map[string]interface{}); ok {
		dist := math.Abs(_numF(tf["atr_dist"], 0))
		if dist > 3 {
			score -= 15
			labels = append(labels, "растянуто "+formatF(dist)+"ATR")
		} else if dist > 2 {
			score -= 7
		}
	}
	return ScoreComponent{Name: "structure", Score: clamp(score, 0, 100), Weight: w.Structure, Reason: join(labels, " / ")}
}

// planQualityScore rates how tradeable a plan in this direction is: ATR% must be in a sane
// band (enough movement, not chaos), RSI must leave room to run (not already exhausted),
// and multi-timeframe trend agreement raises the quality. Replaces the old fixed stub.
func planQualityScore(timeframes map[string]interface{}, direction string, w ScoreWeights) ScoreComponent {
	if direction != "long" && direction != "short" {
		return ScoreComponent{Name: "plan_quality", Score: 45, Weight: w.PlanQuality, Reason: "Нет активного направления."}
	}
	reasons := []string{}
	// ATR% sanity on 1h.
	atrPct := 0.0
	if tf, ok := timeframes["1h"].(map[string]interface{}); ok {
		if c := _numF(tf["close"], 0); c > 0 {
			atrPct = _numF(tf["atr14"], 0) / c * 100
		}
	}
	atrScore := 50.0
	switch {
	case atrPct >= 0.5 && atrPct <= 4:
		atrScore = 85
	case atrPct > 4 && atrPct <= 6:
		atrScore = 60
	case atrPct > 6:
		atrScore = 35
	case atrPct < 0.3:
		atrScore = 40
	}
	reasons = append(reasons, "atr%1h="+formatF(atrPct))
	// RSI headroom on 1h.
	rsi := 50.0
	if tf, ok := timeframes["1h"].(map[string]interface{}); ok {
		rsi = _numF(tf["rsi14"], 50)
	}
	rsiScore := 60.0
	if direction == "long" {
		switch {
		case rsi >= 45 && rsi <= 65:
			rsiScore = 85
		case rsi < 45:
			rsiScore = 65
		case rsi <= 72:
			rsiScore = 55
		default:
			rsiScore = 30
		}
	} else {
		switch {
		case rsi >= 35 && rsi <= 55:
			rsiScore = 85
		case rsi > 55:
			rsiScore = 65
		case rsi >= 28:
			rsiScore = 55
		default:
			rsiScore = 30
		}
	}
	reasons = append(reasons, "rsi1h="+formatF(rsi))
	// Multi-timeframe trend agreement.
	agree := 0
	for _, name := range []string{"15m", "1h", "4h"} {
		if tf, ok := timeframes[name].(map[string]interface{}); ok {
			t, _ := tf["trend"].(string)
			if (direction == "long" && t == "bullish") || (direction == "short" && t == "bearish") {
				agree++
			}
		}
	}
	trendScore := 40.0 + float64(agree)*20 // 40 / 60 / 80 / 100
	reasons = append(reasons, "align="+strconv.Itoa(agree)+"/3")
	score := atrScore*0.4 + rsiScore*0.35 + trendScore*0.25
	return ScoreComponent{Name: "plan_quality", Score: clamp(score, 0, 100), Weight: w.PlanQuality, Reason: join(reasons, " / ")}
}

func volumeScore(timeframes map[string]interface{}, w ScoreWeights) ScoreComponent {
	tf15 := map[string]interface{}{}
	if v, ok := timeframes["15m"].(map[string]interface{}); ok {
		tf15 = v
	}
	vol := _numF(tf15["volume"], 0)
	sma := _numF(tf15["volume_sma20"], 0)
	if sma == 0 {
		return ScoreComponent{Name: "volume", Score: 50, Weight: w.Volume, Reason: "Volume SMA is unavailable."}
	}
	ratio := vol / sma
	score := 58.0
	if ratio >= 1.05 && ratio <= 2.8 {
		score = 78
	} else if ratio > 2.8 {
		score = 62
	}
	return ScoreComponent{Name: "volume", Score: score, Weight: w.Volume, Reason: "ratio=" + formatF(ratio)}
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func mathRound(f float64, prec int) float64 {
	p := 1.0
	for i := 0; i < prec; i++ {
		p *= 10
	}
	return float64(int(f*p+0.5)) / p
}

func join(parts []string, sep string) string {
	res := ""
	for i, p := range parts {
		if i > 0 {
			res += sep
		}
		res += p
	}
	return res
}

func formatF(f float64) string {
	return strconv.FormatFloat(f, 'f', 2, 64)
}
