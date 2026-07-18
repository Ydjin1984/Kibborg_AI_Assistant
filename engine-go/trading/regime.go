package trading

import (
	"math"
	"strings"
)

// RegimeResult mirrors the Python classify_regime output.
type RegimeResult struct {
	Status     string                 `json:"status"`
	Regime     string                 `json:"regime"`
	Confidence float64                `json:"confidence"`
	Trends     map[string]string      `json:"trends"`
	Metrics    map[string]interface{} `json:"metrics"`
	Reasons    []string               `json:"reasons"`
	Flags      []map[string]string    `json:"flags"`
}

func _num(v interface{}, def float64) float64 {
	if v == nil {
		return def
	}
	switch val := v.(type) {
	case float64:
		return val
	case float32:
		return float64(val)
	case int:
		return float64(val)
	case int64:
		return float64(val)
	default:
		return def
	}
}

func ClassifyRegime(timeframes map[string]interface{}, opts ...map[string]interface{}) RegimeResult {
	usable := map[string]interface{}{}
	for name, frame := range timeframes {
		if f, ok := frame.(map[string]interface{}); ok {
			if _, has := f["close"]; has {
				usable[name] = frame
			}
		}
	}
	if len(usable) == 0 {
		return RegimeResult{
			Status:     "NO_DATA",
			Regime:     "unknown",
			Confidence: 0.0,
			Trends:     map[string]string{},
			Metrics:    map[string]interface{}{},
			Reasons:    []string{"No usable timeframe data."},
			Flags:      []map[string]string{{"code": "NO_REGIME_DATA", "severity": "warning", "message": "No usable timeframe data for regime classification.", "source": "regime"}},
		}
	}

	trends := map[string]string{}
	for _, name := range []string{"15m", "1h", "4h"} {
		trend := "range"
		if tf, ok := timeframes[name].(map[string]interface{}); ok {
			if t, ok := tf["trend"].(string); ok {
				trend = t
			}
		}
		trends[name] = trend
	}

	tf15 := map[string]interface{}{}
	if v, ok := timeframes["15m"].(map[string]interface{}); ok {
		tf15 = v
	}
	price := _num(tf15["close"], 0)
	atr := _num(tf15["atr14"], 0)
	atrPct := 0.0
	if price > 0 {
		atrPct = (atr / price) * 100
	}
	changePct := _num(tf15["change_pct"], 0)
	volume := _num(tf15["volume"], 0)
	volumeSma := _num(tf15["volume_sma20"], 0)
	volumeRatio := 0.0
	if volumeSma > 0 {
		volumeRatio = volume / volumeSma
	}

	eventStatus := "OK"
	orderflowBias := "unknown"
	sweepAbove := 0.0
	sweepBelow := 0.0
	fundingRate := 0.0
	oiChange := 0.0

	// Optional contexts (simplified for reference)
	if len(opts) > 0 {
		if ec, ok := opts[0]["event_context"].(map[string]interface{}); ok {
			if s, ok := ec["status"].(string); ok {
				eventStatus = strings.ToUpper(s)
			}
		}
		if of, ok := opts[0]["orderflow"].(map[string]interface{}); ok {
			if b, ok := of["bias"].(string); ok {
				orderflowBias = strings.ToLower(b)
			}
		}
		if lm, ok := opts[0]["liquidity_map"].(map[string]interface{}); ok {
			if s, ok := lm["sweep_probability"].(map[string]interface{}); ok {
				sweepAbove = _num(s["above"], 0)
				sweepBelow = _num(s["below"], 0)
			}
		}
		if fc, ok := opts[0]["funding_context"].(map[string]interface{}); ok {
			fundingRate = _num(fc["last_funding_rate"], 0)
			oiChange = _num(fc["open_interest_change_15m_window_pct"], 0)
		}
	}

	flags := []map[string]string{}
	reasons := []string{}
	bullish := 0
	bearish := 0
	ranges := 0
	for _, t := range trends {
		switch t {
		case "bullish":
			bullish++
		case "bearish":
			bearish++
		default:
			ranges++
		}
	}

	panicEvent := eventStatus == "HIGH_RISK"
	panicVol := atrPct >= 5.0 && volumeRatio >= 2.0 && (math.Abs(changePct) >= 2.5 || orderflowBias == "sell_pressure" || orderflowBias == "buy_pressure")
	squeezeRisk := math.Abs(fundingRate) >= 0.001 || math.Abs(oiChange) >= 12 || math.Max(sweepAbove, sweepBelow) >= 0.72

	var regime string
	var conf float64

	if panicEvent || panicVol {
		regime = "panic"
		conf = 0.84
		reasons = append(reasons, "High-risk event or volatility/volume shock detected.")
		flags = append(flags, map[string]string{"code": "PANIC_REGIME", "severity": "warning", "message": "Panic regime detected; avoid normal entries and reduce size.", "source": "regime"})
	} else if squeezeRisk {
		regime = "squeeze"
		conf = 0.76
		reasons = append(reasons, "Funding/OI or liquidity sweep risk points to squeeze conditions.")
		flags = append(flags, map[string]string{"code": "SQUEEZE_REGIME", "severity": "warning", "message": "Squeeze risk is elevated; wait for confirmation after liquidation/sweep.", "source": "regime"})
	} else if atrPct >= 3.5 {
		regime = "volatile"
		conf = 0.72
		reasons = append(reasons, "ATR is elevated against current price.")
		flags = append(flags, map[string]string{"code": "VOLATILE_REGIME", "severity": "warning", "message": "ATR is elevated; reduce confidence and size.", "source": "regime"})
	} else if bullish >= 2 && trends["4h"] == "bullish" {
		regime = "trend_up"
		conf = 0.78
		if bullish == 3 {
			conf += 0.08
		}
		reasons = append(reasons, "Most core timeframes are bullish and 4h confirms.")
	} else if bearish >= 2 && trends["4h"] == "bearish" {
		regime = "trend_down"
		conf = 0.78
		if bearish == 3 {
			conf += 0.08
		}
		reasons = append(reasons, "Most core timeframes are bearish and 4h confirms.")
	} else if ranges >= 2 {
		regime = "range"
		conf = 0.68
		reasons = append(reasons, "Most core timeframes are range-bound.")
		flags = append(flags, map[string]string{"code": "RANGE_REGIME", "severity": "warning", "message": "Market is range-bound; prefer breakout/retest confirmation.", "source": "regime"})
	} else {
		regime = "transition"
		conf = 0.58
		reasons = append(reasons, "Core timeframes disagree.")
		flags = append(flags, map[string]string{"code": "TRANSITION_REGIME", "severity": "warning", "message": "Timeframes disagree; avoid high-conviction entries.", "source": "regime"})
	}

	if volumeRatio > 3 {
		flags = append(flags, map[string]string{"code": "VOLUME_SPIKE", "severity": "warning", "message": "15m volume is unusually high; expect slippage and fakeouts.", "source": "regime"})
	}

	metrics := map[string]interface{}{
		"atr_pct_15m":             math.Round(atrPct*1000) / 1000,
		"change_pct_15m":          math.Round(changePct*1000) / 1000,
		"volume_ratio_15m":        nil,
		"sweep_probability_above": math.Round(sweepAbove*1000) / 1000,
		"sweep_probability_below": math.Round(sweepBelow*1000) / 1000,
		"funding_rate":            math.Round(fundingRate*1000000) / 1000000,
		"oi_change_pct":           math.Round(oiChange*1000) / 1000,
	}
	if volumeSma > 0 {
		metrics["volume_ratio_15m"] = math.Round(volumeRatio*1000) / 1000
	}

	return RegimeResult{
		Status:     "OK",
		Regime:     regime,
		Confidence: math.Min(math.Round(conf*1000)/1000, 0.95),
		Trends:     trends,
		Metrics:    metrics,
		Reasons:    reasons,
		Flags:      flags,
	}
}
