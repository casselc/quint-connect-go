package qload

import (
	"strings"
	"testing"
)

// A recovery verdict against a baseline far below the offered load says
// what to do: set Experiment.Healthy.
func TestRecoveryWarning(t *testing.T) {
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
}
