package qmut_test

import (
	"context"
	"testing"
	"time"

	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qmut"
)

// Woven code calls Cond and Pick from a run's tasks: the run's mutant
// changes them, the others' runs and code outside a run see the original.
func TestSwitchesFollowTheRun(t *testing.T) {
	base := qmut.Register([]qmut.Mutant{{Op: "negate if"}, {Op: "<= for <"}})
	run := func(ctx context.Context) (cond, pick bool) {
		sim := qenv.New(qenv.WithContext(qenv.NewSeeded(1), ctx))
		sim.Process("p", false, func(context.Context) {
			cond, pick = qmut.Cond(base, true), qmut.Pick(base+1, false, true)
		})
		if err := sim.Run(time.Minute); err != nil {
			t.Fatal(err)
		}
		sim.Close()
		return
	}
	r := &qmut.Reach{}
	if c, p := run(qmut.WithReach(context.Background(), r)); !c || p {
		t.Errorf("no mutant: Cond %v, Pick %v; want the originals", c, p)
	}
	if !r.Reached(base) || !r.Reached(base+1) {
		t.Error("reach did not note both mutants")
	}
	if c, _ := run(qmut.WithMutant(context.Background(), base)); c {
		t.Error("mutant on: Cond not negated")
	}
	if _, p := run(qmut.WithMutant(context.Background(), base+1)); !p {
		t.Error("mutant on: Pick not mutated")
	}
	if !qmut.Cond(base, true) {
		t.Error("outside a run: Cond changed")
	}
}
