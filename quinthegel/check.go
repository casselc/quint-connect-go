package quinthegel

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/casselc/quint-connect-go/qtrace"
	"github.com/casselc/quint-connect-go/validate"
	"hegel.dev/go/hegel"
)

// Verdict is the outcome of checking one run.
type Verdict struct {
	OK      bool
	Summary string // the validator's report (or what the cache remembered of it)
	Cached  bool   // decided from earlier checks, without running quint
}

// CaseChecker checks the runs of Hegel test cases against the model. It
// remembers verdicts, so shrinking does not run quint again for runs it has
// already decided:
//
//   - an identical run (same steps, same instance constants) gets the same
//     verdict;
//   - a run whose prefix up to a step the model rejected (a step that is
//     not a transition, a state mismatch, or an invariant violation after
//     it) matches an earlier failing run's, with the same constants, fails
//     the same way: those verdicts depend only on that prefix.
//
// Prefix reuse is limited to runs of one actor with no domain clocks
// (quint.order.*), whose reconstructed order is the recording order.
type CaseChecker struct {
	Checker *validate.Checker

	mu       sync.Mutex
	full     map[string]Verdict // constants + all steps -> verdict
	prefixes map[string]string  // constants + failing prefix -> summary
	n        atomic.Int64       // quint runs
	hits     atomic.Int64
}

// NewCaseChecker wraps a validate.Checker (see Checker).
func NewCaseChecker(c *validate.Checker) *CaseChecker {
	return &CaseChecker{Checker: c, full: map[string]Verdict{}, prefixes: map[string]string{}}
}

// Stats reports how many runs were checked with quint and how many were
// decided from the cache.
func (cc *CaseChecker) Stats() (quintRuns, cacheHits int64) { return cc.n.Load(), cc.hits.Load() }

// Check decides one run. A run with no steps is vacuously OK.
func (cc *CaseChecker) Check(ctx context.Context, steps []qtrace.Step) (Verdict, error) {
	if len(steps) == 0 {
		return Verdict{OK: true, Summary: "no steps recorded"}, nil
	}
	consts, prefixable := cc.constants(steps)
	// Steps the binding skips become no model call; with one process they
	// cannot change the replay either, so they are left out of the keys:
	// shrink candidates that differ only in refused calls share verdicts.
	eff, effIdx := steps, identity(len(steps))
	if prefixable {
		eff, effIdx = cc.effective(steps)
	}
	keys := prefixKeys(consts, eff)
	full := keys[len(keys)-1]
	cc.mu.Lock()
	if v, ok := cc.full[full]; ok {
		cc.mu.Unlock()
		cc.hits.Add(1)
		v.Cached = true
		return v, nil
	}
	if prefixable {
		for i, k := range keys {
			if sum, ok := cc.prefixes[k]; ok {
				cc.mu.Unlock()
				cc.hits.Add(1)
				return Verdict{Summary: fmt.Sprintf("the first %d steps are a prefix the model already rejected:\n%s", i+1, sum), Cached: true}, nil
			}
		}
	}
	cc.mu.Unlock()

	c := *cc.Checker
	c.Options.Dir = filepath.Join(cc.Checker.Options.Dir, fmt.Sprintf("check%d", cc.n.Add(1)))
	rep, err := c.Check(ctx, steps)
	if err != nil {
		return Verdict{}, err
	}
	v := Verdict{OK: rep.OK(), Summary: rep.Summary()}
	cc.mu.Lock()
	defer cc.mu.Unlock()
	cc.full[full] = v
	if !v.OK && prefixable && len(rep.Actors) == 1 && len(rep.Issues) == 0 {
		if at := failingStep(rep.Actors[0], steps); at >= 0 {
			// the prefix key up to the failing step, in effective steps
			for j := len(effIdx) - 1; j >= 0; j-- {
				if effIdx[j] <= at {
					if effIdx[j] == at {
						cc.prefixes[keys[j]] = v.Summary
					}
					break
				}
			}
		}
	}
	return v, nil
}

// constants renders the run's instance constants (they may depend on the
// whole run) and says whether prefix reuse is sound for it.
func (cc *CaseChecker) constants(steps []qtrace.Step) (string, bool) {
	traces, issues := qtrace.Reconstruct(steps)
	if len(traces) != 1 || len(issues) > 0 || len(traces[0].Issues) > 0 {
		return "", false
	}
	for _, s := range steps {
		if len(s.Order) > 0 {
			return "", false
		}
	}
	p, err := cc.Checker.Binding.Plan(cc.Checker.Model, traces[0])
	if err != nil {
		return "", false
	}
	return strings.Join(p.ConstantList(), ", "), true
}

// effective drops the steps the binding skips, when the run has a single
// process (a skipped step can still mark a restart between processes). It
// returns the kept steps and their indexes in steps.
func (cc *CaseChecker) effective(steps []qtrace.Step) ([]qtrace.Step, []int) {
	for i := range steps {
		if steps[i].Process != steps[0].Process {
			return steps, identity(len(steps))
		}
	}
	var out []qtrace.Step
	var idx []int
	for i := range steps {
		if !cc.Checker.Binding.Skips(&steps[i]) {
			out = append(out, steps[i])
			idx = append(idx, i)
		}
	}
	return out, idx
}

func identity(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

// failingStep is the index in steps of the step after which the verdict
// became a failure: the conformance failure, else the earliest invariant
// violation. -1 if it is not an observed step.
func failingStep(r *validate.Result, steps []qtrace.Step) int {
	var f *validate.Failure
	if r.Failure != nil {
		f = r.Failure
	} else {
		for _, inv := range r.Invariants {
			if inv.Failure != nil && (f == nil || (inv.Failure.Index >= 0 && inv.Failure.Index < f.Index)) {
				f = inv.Failure
			}
		}
	}
	if f == nil || f.Step == nil {
		return -1
	}
	for i := range steps {
		if steps[i].Seq == f.Step.Seq && steps[i].Recorder == f.Step.Recorder {
			return i
		}
	}
	return -1
}

// ids numbers opaque identities (processes, threads) by first appearance:
// recorders are fresh per test case, so their ids differ between runs that
// are the same run for the model.
type ids map[string]int

func (m ids) of(s string) int {
	if n, ok := m[s]; ok {
		return n
	}
	m[s] = len(m) + 1
	return m[s]
}

func stepKey(s *qtrace.Step, procs, threads ids) string {
	return fmt.Sprintf("%s|p%d|t%d|%s|%s|%v;", s.Actor, procs.of(s.Process), threads.of(s.Thread), s.Outcome, s.String(), s.Obs)
}

// prefixKeys returns the cache key of every prefix of steps (keys[i]
// covers steps[:i+1]); with no steps, one key for the empty run.
func prefixKeys(consts string, steps []qtrace.Step) []string {
	var b strings.Builder
	b.WriteString(consts)
	b.WriteString("#")
	if len(steps) == 0 {
		return []string{b.String()}
	}
	keys := make([]string, 0, len(steps))
	procs, threads := ids{}, ids{}
	for i := range steps {
		b.WriteString(stepKey(&steps[i], procs, threads))
		keys = append(keys, b.String())
	}
	return keys
}

// CheckCase checks a test case's recorded run and fails the test case
// (tc.Errorf) if the model rejects it, so Hegel shrinks the run against the
// model. It is what Test does in Each mode, for use in hand-written Hegel
// tests (hegel.Test, hegel.Run):
//
//	cc := quinthegel.NewCaseChecker(checker)
//	err := hegel.Run(func(tc hegel.TestCase) {
//		rec := quinthegel.NewRecorder(tc)
//		hegel.RunStateful(tc, &machine{rec: rec})
//		quinthegel.CheckCase(tc, cc, rec.Steps())
//	})
func CheckCase(tc hegel.TestCase, cc *CaseChecker, steps []qtrace.Step) {
	v, err := cc.Check(context.Background(), steps)
	if err != nil {
		tc.Errorf("quinthegel: checking the run: %v", err)
		return
	}
	if !v.OK {
		tc.Errorf("quinthegel: the run does not satisfy the model:\n%s", v.Summary)
	}
}
