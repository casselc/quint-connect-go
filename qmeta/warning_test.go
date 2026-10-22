package qmeta

import (
	"strings"
	"testing"
)

// A calibrated arrival rate above capacity says what to do: hold λ.
// (Recovery's warning is qload's.)
func TestWarnings(t *testing.T) {
	if w := (Params{Lambda: 10.3, Mu: 10}).Warning(); !strings.Contains(w, "CalibrateTimeout") {
		t.Errorf("λ ≥ μ: %q", w)
	}
	if w := (Params{Lambda: 9.5, Mu: 10}).Warning(); w != "" {
		t.Errorf("λ < μ: %q", w)
	}
}
