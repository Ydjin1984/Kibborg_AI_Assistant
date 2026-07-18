package trading

import (
	"math"
	"testing"
)

func TestAssessTradePlanOK(t *testing.T) {
	// entry 100, stop 98.5 (1.5% away), best target 106 → R/R = 6/1.5 = 4.0.
	r := AssessTradePlan("long", []float64{100}, 98.5, true, []float64{102.25, 103.75, 106}, 1.5)
	if r.Status != "ok" {
		t.Errorf("clean plan status = %q, want ok (%+v)", r.Status, r)
	}
	if !r.HasRR || math.Abs(r.RR-4.0) > 1e-6 {
		t.Errorf("R/R = %v (has=%v), want 4.0", r.RR, r.HasRR)
	}
	if math.Abs(r.StopDistancePct-1.5) > 1e-6 {
		t.Errorf("stop distance = %v%%, want 1.5", r.StopDistancePct)
	}
}

func TestAssessTradePlanInvalidStopSide(t *testing.T) {
	// Long with stop ABOVE entry is geometrically invalid → reject.
	r := AssessTradePlan("long", []float64{100}, 105, true, []float64{110}, 1.5)
	if r.Status != "reject" {
		t.Errorf("stop above entry for long = %q, want reject", r.Status)
	}
}

func TestAssessTradePlanLowRR(t *testing.T) {
	// entry 100, stop 95, target 101 → R/R 0.2, well below the 1.5 minimum → reject.
	r := AssessTradePlan("long", []float64{100}, 95, true, []float64{101}, 1.5)
	if r.Status != "reject" {
		t.Errorf("low R/R = %q, want reject", r.Status)
	}
}

func TestAssessTradePlanWideStop(t *testing.T) {
	// entry 100, stop 90 (10% away), target 130 → R/R 3.0 but stop is wide → warning.
	r := AssessTradePlan("long", []float64{100}, 90, true, []float64{130}, 1.5)
	if r.Status != "warning" {
		t.Errorf("wide stop = %q, want warning (%+v)", r.Status, r)
	}
	found := false
	for _, f := range r.Flags {
		if f.Code == "wide_stop" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a wide_stop flag, got %+v", r.Flags)
	}
}

func TestAssessTradePlanNonDirectional(t *testing.T) {
	r := AssessTradePlan("wait/range", nil, 0, false, nil, 1.5)
	if r.Status != "wait" {
		t.Errorf("non-directional = %q, want wait", r.Status)
	}
}
