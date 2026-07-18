package trading

import (
	"math"
	"strings"
	"testing"
)

func scoringFrames() map[string]interface{} {
	mk := func(trend, swing string, rsi, hist float64) map[string]interface{} {
		return map[string]interface{}{
			"close":        100.0,
			"trend":        trend,
			"swing":        swing,
			"atr14":        1.5,
			"atr_dist":     0.5,
			"rsi14":        rsi,
			"macd":         map[string]interface{}{"histogram": hist},
			"volume":       150.0,
			"volume_sma20": 100.0,
		}
	}
	return map[string]interface{}{
		"15m": mk("bullish", "up", 55, 1.0),
		"1h":  mk("bullish", "up", 55, 1.0),
		"4h":  mk("bullish", "up", 55, 1.0),
	}
}

func TestStructureScore(t *testing.T) {
	// All swings "up" for a long, no overextension → high structure score.
	c := structureScore(scoringFrames(), "long", DefaultWeights)
	if c.Name != "structure" || c.Score < 85 {
		t.Errorf("aligned up-structure long = %+v, want ≥85", c)
	}
	// Same market, short direction → swings oppose → low score.
	cs := structureScore(scoringFrames(), "short", DefaultWeights)
	if cs.Score > 30 {
		t.Errorf("up-structure scored high for short: %v", cs.Score)
	}
	// Overextension penalty: price 4 ATR above EMA20 knocks the score down.
	over := scoringFrames()
	over["1h"].(map[string]interface{})["atr_dist"] = 4.0
	co := structureScore(over, "long", DefaultWeights)
	if co.Score >= c.Score {
		t.Errorf("overextended (%v) should score below clean (%v)", co.Score, c.Score)
	}
}

func TestPlanQualityScore(t *testing.T) {
	c := planQualityScore(scoringFrames(), "long", DefaultWeights)
	if c.Name != "plan_quality" || c.Score < 60 {
		t.Errorf("healthy long plan = %+v, want ≥60", c)
	}
	// Overbought RSI (exhausted) drops plan quality.
	ob := scoringFrames()
	for _, tf := range []string{"15m", "1h", "4h"} {
		ob[tf].(map[string]interface{})["rsi14"] = 85.0
	}
	co := planQualityScore(ob, "long", DefaultWeights)
	if co.Score >= c.Score {
		t.Errorf("overbought (%v) should score below healthy (%v)", co.Score, c.Score)
	}
	// No stub text leaks into the reason.
	if strings.Contains(c.Reason, "stub") {
		t.Errorf("plan_quality reason still stubbed: %q", c.Reason)
	}
}

func TestScoreLongComponents(t *testing.T) {
	b := ScoreLong(scoringFrames(), map[string]interface{}{"direction": "long"})
	if b.Direction != "long" {
		t.Errorf("direction = %q", b.Direction)
	}
	if len(b.Components) != 5 {
		t.Fatalf("want 5 components, got %d", len(b.Components))
	}
	// All frames aligned → trend component is a full 100.
	if b.Components[0].Name != "trend_alignment" || b.Components[0].Score != 100 {
		t.Errorf("trend_alignment = %+v", b.Components[0])
	}
	// RSI 55 (sweet spot) + positive MACD → momentum 85*0.65 + 75*0.35 = 81.5 per frame.
	if math.Abs(b.Components[1].Score-81.5) > 1e-9 {
		t.Errorf("momentum = %v, want 81.5", b.Components[1].Score)
	}
	// Volume ratio 1.5 lands in the healthy band.
	if b.Components[2].Score != 78 {
		t.Errorf("volume = %v, want 78", b.Components[2].Score)
	}
	// Final = weighted sum of components, clamped to [0,100].
	want := 0.0
	for _, c := range b.Components {
		want += c.Score * c.Weight
	}
	if math.Abs(b.FinalScore-mathRound(want, 2)) > 1e-9 {
		t.Errorf("final = %v, want %v", b.FinalScore, mathRound(want, 2))
	}
	// The reasons carry REAL numbers now, not the old "stub" placeholder.
	if strings.Contains(b.Components[1].Reason, "stub") {
		t.Errorf("momentum reason still contains stub: %q", b.Components[1].Reason)
	}
}

func TestVolumeScoreUnavailable(t *testing.T) {
	c := volumeScore(map[string]interface{}{"15m": map[string]interface{}{"volume": 10.0}}, DefaultWeights)
	if c.Score != 50 {
		t.Errorf("no SMA → score %v, want neutral 50", c.Score)
	}
}

func TestDirectionParsing(t *testing.T) {
	if got := _direction(map[string]interface{}{"direction": "LONG"}); got != "long" {
		t.Errorf("_direction(LONG) = %q", got)
	}
	if got := _direction(nil); got != "wait/range" {
		t.Errorf("_direction(nil) = %q", got)
	}
	if got := _direction(map[string]interface{}{"direction": "sideways"}); got != "wait/range" {
		t.Errorf("_direction(sideways) = %q", got)
	}
}

func TestMathRound(t *testing.T) {
	if got := mathRound(1.234, 2); got != 1.23 {
		t.Errorf("mathRound(1.234, 2) = %v", got)
	}
	if got := mathRound(1.235, 2); got != 1.24 {
		t.Errorf("mathRound(1.235, 2) = %v", got)
	}
	if got := mathRound(0.9995, 3); got != 1.0 {
		t.Errorf("mathRound(0.9995, 3) = %v", got)
	}
}

func TestClamp(t *testing.T) {
	if clamp(-5, 0, 100) != 0 || clamp(150, 0, 100) != 100 || clamp(42, 0, 100) != 42 {
		t.Error("clamp misbehaves")
	}
}
