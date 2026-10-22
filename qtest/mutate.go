package qtest

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qmut"
)

// A MutationReport is what exploring the woven mutants found.
type MutationReport struct {
	// Unreached are mutants whose code no run of the baseline reached:
	// coverage's business, not the model's.
	Unreached []qmut.Mutant
	// Killed are mutants some run of whose exploration the model rejected,
	// or that broke a run (an error, a panic).
	Killed []qmut.Mutant
	// Survived are mutants reached and explored that every run survived:
	// code the model does not constrain. Each is a question: is the code
	// needed, or is the model missing what it is for?
	Survived []qmut.Mutant
}

// Score is the share of reached mutants killed.
func (r *MutationReport) Score() float64 {
	n := len(r.Killed) + len(r.Survived)
	if n == 0 {
		return 1
	}
	return float64(len(r.Killed)) / float64(n)
}

func (r *MutationReport) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "mutants: %d reached, %d killed (%.0f%%), %d survived, %d unreached\n",
		len(r.Killed)+len(r.Survived), len(r.Killed), 100*r.Score(), len(r.Survived), len(r.Unreached))
	for _, m := range r.Survived {
		fmt.Fprintf(&b, "  survived: %s\n", m)
	}
	return b.String()
}

// Mutate explores the system once per woven mutant (qweave -mutate; with
// none woven it skips): a baseline exploration with every mutant off,
// which must conform and says which mutants' code is reached, then each
// reached mutant (keep, nil: all) with cfg's seeds. A mutant is killed
// when some run is rejected by the model, errs or panics. Verdicts are
// shared across the explorations (VerdictCache): most mutants' runs repeat
// the baseline's steps.
func (s System) Mutate(t *testing.T, cfg ExploreConfig, keep func(qmut.Mutant) bool) *MutationReport {
	t.Helper()
	all := qmut.Mutants()
	if len(all) == 0 {
		t.Skip("no mutants woven: run with qweave -mutate")
	}
	if cfg.Verdicts == nil {
		cfg.Verdicts = &VerdictCache{}
	}
	cfg.Opts = s.opts(cfg.Opts...)
	reach := &qmut.Reach{}
	base := explore(t, s.Binding, cfg, func(c qenv.Chooser) error {
		return s.Run(qenv.WithContext(c, qmut.WithReach(qenv.ContextOf(c), reach)))
	})
	base.Expect(t, Conforms())
	rep := &MutationReport{}
	for _, m := range all {
		if keep != nil && !keep(m) {
			continue
		}
		if !reach.Reached(m.ID) {
			rep.Unreached = append(rep.Unreached, m)
			continue
		}
		var broke atomic.Bool
		x := explore(t, s.Binding, cfg, func(c qenv.Chooser) (err error) {
			defer func() {
				if r := recover(); r != nil {
					broke.Store(true) // a run the mutant crashed: killed
				}
			}()
			if s.Run(qenv.WithContext(c, qmut.WithMutant(qenv.ContextOf(c), m.ID))) != nil {
				broke.Store(true)
			}
			return nil
		})
		if broke.Load() || len(x.Failing()) > 0 {
			rep.Killed = append(rep.Killed, m)
		} else {
			rep.Survived = append(rep.Survived, m)
		}
	}
	return rep
}
