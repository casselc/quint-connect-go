package qload

import (
	"fmt"
	"strings"
	"testing"
	"time"
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

type fatalTB struct{ msg string }

func (f *fatalTB) Helper()                           {}
func (f *fatalTB) Fatalf(format string, args ...any) { f.msg = fmt.Sprintf(format, args...) }

// Assess without a healthy goodput is not a verdict; Expect says what was
// wanted and what happened.
func TestRecoveryExpect(t *testing.T) {
	r := Assess(Experiment{Rate: 1}, "done", Trigger{}, time.Second, 1)
	f := &fatalTB{}
	r.Expect(f, true)
	if !strings.Contains(f.msg, "Experiment.Healthy is not set") {
		t.Errorf("unset Healthy: %q", f.msg)
	}
	stuck := Recovery{Before: 10, After: 1, Recovered: false}
	f = &fatalTB{}
	stuck.Expect(f, true)
	if !strings.Contains(f.msg, "want recovery") {
		t.Errorf("stuck: %q", f.msg)
	}
	f = &fatalTB{}
	stuck.Expect(f, false)
	if f.msg != "" {
		t.Errorf("a metastable failure, as wanted: %q", f.msg)
	}
}
