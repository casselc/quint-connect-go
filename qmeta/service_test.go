package qmeta_test

import (
	"math/rand/v2"
	"testing"
	"time"

	"github.com/casselc/quint-connect-go/qmeta"
)

// Exponential samples fit the chain's assumption; constant ones do not; the
// rate is the mean's inverse, and the sampler draws the measured times.
func TestMeasure(t *testing.T) {
	rnd := rand.New(rand.NewPCG(1, 2))
	var exp, flat []time.Duration
	for range 5000 {
		exp = append(exp, time.Duration(rnd.ExpFloat64()*float64(100*time.Millisecond)))
		flat = append(flat, 100*time.Millisecond)
	}
	e, f := qmeta.Measure(exp), qmeta.Measure(flat)
	if !e.Exponential() || f.Exponential() || f.CV != 0 {
		t.Errorf("exponential CV %.2f, constant CV %.2f", e.CV, f.CV)
	}
	if mu := e.Mu(); mu < 9.5 || mu > 10.5 {
		t.Errorf("mu %.2f, want about 10", mu)
	}
	if d := f.Draw(rnd); d != 100*time.Millisecond {
		t.Errorf("drew %v", d)
	}
}
