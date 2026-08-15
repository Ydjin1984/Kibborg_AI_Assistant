package main

import (
	"strings"
	"testing"

	"kibborg/engine/trading"
)

func TestRenderRSIIsFilterNotEntry(t *testing.T) {
	var b strings.Builder
	renderRSI(&b, trading.RSIReport{
		Period:      14,
		SMAPeriod:   9,
		Regime:      "trend_up",
		ZoneLow:     40,
		ZoneHigh:    80,
		ZoneNote:    "в бычьем тренде рабочий диапазон 40–80",
		Value:       78.4,
		Slope:       1.2,
		SMA:         71.0,
		AllowLong:   true,
		AllowShort:  false,
		MultiTF:     map[string]float64{"15m": 63, "1h": 68, "4h": 74, "1d": 78.4},
		MultiBias:   "bullish",
		MFI:         61,
		MFISlope:    -2,
		MFIRSISplit: "volume_lag",
		Scenarios: []trading.RSIScenario{
			{ID: "divergence", Status: "none", Text: "классической дивергенции нет"},
			{ID: "trend_filter", Status: "strength", Text: "RSI выше 70 в бычьем тренде — сила"},
			{ID: "extreme_return", Status: "n/a", Text: "не для тренда"},
		},
		Verdict: "SHORT по RSI запрещён: высокий RSI в восходящем тренде — сила, не разворот",
	})
	got := b.String()
	for _, must := range []string{
		"RSI-контекст",
		"фильтр, не вход",
		"40–80",
		"78.4",
		"MFI",
		"денег в движении нет",
		"мульти-ТФ",
		"шорт запрещён фильтром",
		"SHORT по RSI запрещён",
	} {
		if !strings.Contains(got, must) {
			t.Errorf("renderRSI missing %q in:\n%s", must, got)
		}
	}
}

func TestRenderReportIncludesRSI(t *testing.T) {
	r := trading.DecisionReport{
		Symbol:    "BTCUSDT",
		Market:    "spot",
		Direction: "long",
		Regime:    "trend_up",
		Meta: map[string]any{
			"rsi": trading.RSIReport{
				Period: 14, Regime: "trend_up", ZoneLow: 40, ZoneHigh: 80,
				Value: 72, Verdict: "высокий RSI в тренде — сила",
				AllowLong: true, AllowShort: false,
			},
		},
	}
	got := renderReport(r)
	if !strings.Contains(got, "RSI-контекст") {
		t.Fatalf("renderReport lost RSI block:\n%s", got)
	}
	if !strings.Contains(got, "высокий RSI в тренде") {
		t.Errorf("verdict missing:\n%s", got)
	}
}

func TestRenderFlowBlock(t *testing.T) {
	var b strings.Builder
	renderFlow(&b, trading.FlowReport{
		Available:  true,
		Side:       "long",
		AllowLong:  true,
		AllowShort: false,
		LongScore:  90,
		ShortScore: 20,
		Snaps: []trading.FlowSnap{{
			TF: "1h", HasOI: true, HasCVD: true, OIChangePct: 1.2, CVDDelta: 15,
			Quadrant: "new_longs", Note: "цена и OI растут — приходят новые лонги",
		}},
		Verdict: "Поток подтверждает ЛОНГ",
	})
	got := b.String()
	for _, must := range []string{"Поток OI / CVD", "новые лонги", "шорт запрещён фильтром", "ЛОНГ"} {
		if !strings.Contains(got, must) {
			t.Errorf("renderFlow missing %q in:\n%s", must, got)
		}
	}
}
