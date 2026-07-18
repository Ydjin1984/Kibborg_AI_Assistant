package trading

import "testing"

func TestCalibrateConfidenceBase(t *testing.T) {
	c := CalibrateConfidence(ConfidenceInputs{Score: 80, RegimeConfidence: 0.8, Decision: "ALLOW"})
	score, _ := c["score"].(float64)
	// base 0.8 + regime bonus (0.8-0.6)*0.12 = 0.024 → ~0.824, label high.
	if score < 0.8 || score > 0.85 {
		t.Errorf("calibrated score = %v, want ~0.82", score)
	}
	if c["label"] != "high" {
		t.Errorf("label = %v, want high", c["label"])
	}
}

func TestCalibrateConfidenceWaitCap(t *testing.T) {
	// A WAIT decision must cap confidence at 0.55 even for a high raw score.
	c := CalibrateConfidence(ConfidenceInputs{Score: 90, Decision: "WAIT"})
	if score, _ := c["score"].(float64); score > 0.55 {
		t.Errorf("WAIT confidence = %v, want ≤ 0.55", score)
	}
}

func TestCalibrateConfidenceWarningsPenalize(t *testing.T) {
	clean := CalibrateConfidence(ConfidenceInputs{Score: 80, Decision: "WATCH"})
	warned := CalibrateConfidence(ConfidenceInputs{Score: 80, Decision: "WATCH", WarningFlags: 3})
	cs, _ := clean["score"].(float64)
	ws, _ := warned["score"].(float64)
	if ws >= cs {
		t.Errorf("warnings should lower confidence: clean=%v warned=%v", cs, ws)
	}
}

func TestCalibrateConfidenceLowFlag(t *testing.T) {
	c := CalibrateConfidence(ConfidenceInputs{Score: 20, Decision: "WAIT"})
	flags, _ := c["flags"].([]map[string]string)
	if len(flags) == 0 {
		t.Error("low calibrated confidence should raise a LOW_CALIBRATED_CONFIDENCE flag")
	}
}
