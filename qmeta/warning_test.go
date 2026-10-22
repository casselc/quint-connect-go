package qmeta

import (
	"strings"
	"testing"
)

// The traps of recovery verdicts say what to do: a baseline far below the
// offered load (set Experiment.Healthy), and a calibrated arrival rate
// above capacity (hold λ).
func TestWarnings(t *testing.T) {
	r := Recovery{Before: 0.4, After: 0.4, Offered: 3, Recovered: true}
	if w := r.Warning(); !strings.Contains(w, "0.4/s against 3.0 offered") || !strings.Contains(w, "Experiment.Healthy") {
		t.Errorf("unhealthy baseline: %q", w)
	}
	if !strings.Contains(r.String(), "recovered") || !strings.Contains(r.String(), "Experiment.Healthy") {
		t.Errorf("String: %q", r.String())
	}
	r.Baseline = 3
	if w := r.Warning(); w != "" {
		t.Errorf("with a healthy baseline set: %q", w)
	}
	if w := (Params{Lambda: 10.3, Mu: 10}).Warning(); !strings.Contains(w, "CalibrateTimeout") {
		t.Errorf("λ ≥ μ: %q", w)
	}
	if w := (Params{Lambda: 9.5, Mu: 10}).Warning(); w != "" {
		t.Errorf("λ < μ: %q", w)
	}
}
