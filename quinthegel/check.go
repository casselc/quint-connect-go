package quinthegel

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/casselc/quint-connect-go/qtrace"
	"github.com/casselc/quint-connect-go/validate"
	"hegel.dev/go/hegel"
)

// verdict is the outcome of checking one run.
type verdict struct {
	OK      bool
	Summary string // the validator's report (or what the cache remembered of it)
	Cached  bool   // decided from earlier checks, without running quint
}

// caseChecker checks the runs of Hegel test cases against the model. It
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
type caseChecker struct {
	Checker *validate.Checker

	mu       sync.Mutex
	full     map[string]verdict // constants + all steps -> verdict
	prefixes map[string]string  // constants + failing prefix -> summary
	n        atomic.Int64       // quint runs
	hits     atomic.Int64
	batches  atomic.Int64
}

// newCaseChecker wraps a validate.Checker (see Checker).
func newCaseChecker(c *validate.Checker) *caseChecker {
	return &caseChecker{Checker: c, full: map[string]verdict{}, prefixes: map[string]string{}}
}

// Stats reports how many runs were checked with quint and how many were
// decided from the cache.
func (cc *caseChecker) Stats() (quintRuns, cacheHits int64) { return cc.n.Load(), cc.hits.Load() }

// keyed is a run with its cache keys.
type keyed struct {
	steps      []qtrace.Step
	keys       []string // prefix keys over the effective steps; the last is the full key
	effIdx     []int    // index in steps of each effective step
	prefixable bool
}

func (cc *caseChecker) key(steps []qtrace.Step) keyed {
	consts, prefixable := cc.constants(steps)
	// Steps the binding skips become no model call; with one process they
	// cannot change the replay either, so they are left out of the keys:
	// shrink candidates that differ only in refused calls share verdicts.
	eff, effIdx := steps, identity(len(steps))
	if prefixable {
		eff, effIdx = cc.effective(steps)
	}
	return keyed{steps: steps, keys: prefixKeys(consts, eff), effIdx: effIdx, prefixable: prefixable}
}

// lookup decides a run from earlier verdicts, if it can.
func (cc *caseChecker) lookup(k keyed) (verdict, bool) {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	if v, ok := cc.full[k.keys[len(k.keys)-1]]; ok {
		v.Cached = true
		return v, true
	}
	if k.prefixable {
		for i, key := range k.keys {
			if sum, ok := cc.prefixes[key]; ok {
				return verdict{Summary: fmt.Sprintf("the first %d steps are a prefix the model already rejected:\n%s", i+1, sum), Cached: true}, true
			}
		}
	}
	return verdict{}, false
}

// store remembers the verdict on a run, and its failing prefix.
func (cc *caseChecker) store(k keyed, rep *validate.Report) verdict {
	v := verdict{OK: rep.OK(), Summary: rep.Summary()}
	cc.mu.Lock()
	defer cc.mu.Unlock()
	cc.full[k.keys[len(k.keys)-1]] = v
	if !v.OK && k.prefixable && len(rep.Actors) == 1 && len(rep.Issues) == 0 {
		if at := failingStep(rep.Actors[0], k.steps); at >= 0 {
			// the prefix key up to the failing step, in effective steps
			for j := len(k.effIdx) - 1; j >= 0; j-- {
				if k.effIdx[j] <= at {
					if k.effIdx[j] == at {
						cc.prefixes[k.keys[j]] = v.Summary
					}
					break
				}
			}
		}
	}
	return v
}

// Check decides one run. A run with no steps is vacuously OK.
func (cc *caseChecker) Check(ctx context.Context, steps []qtrace.Step) (verdict, error) {
	if len(steps) == 0 {
		return verdict{OK: true, Summary: "no steps recorded"}, nil
	}
	k := cc.key(steps)
	if v, ok := cc.lookup(k); ok {
		cc.hits.Add(1)
		return v, nil
	}
	c := *cc.Checker
	c.Options.Dir = filepath.Join(cc.Checker.Options.Dir, fmt.Sprintf("check%d", cc.n.Add(1)))
	c.Options.NoITF = true // verdicts only
	rep, err := c.Check(ctx, steps)
	if err != nil {
		return verdict{}, err
	}
	return cc.store(k, rep), nil
}

// Prefetch decides many runs at once (validate.Checker.CheckBatch: one
// quint process per instance and chunk, not per run) and remembers the
// verdicts, so later Checks of these runs, or of runs sharing a rejected
// prefix, need no quint. It returns the verdicts, in order (OK for an
// empty run), and counts one quint run per process.
func (cc *caseChecker) Prefetch(ctx context.Context, runs [][]qtrace.Step) ([]verdict, error) {
	out := make([]verdict, len(runs))
	ks := make([]keyed, len(runs))
	var todo [][]qtrace.Step
	var idx []int
	for i, steps := range runs {
		if len(steps) == 0 {
			out[i] = verdict{OK: true, Summary: "no steps recorded"}
			continue
		}
		ks[i] = cc.key(steps)
		if v, ok := cc.lookup(ks[i]); ok {
			cc.hits.Add(1)
			out[i] = v
			continue
		}
		todo = append(todo, steps)
		idx = append(idx, i)
	}
	if len(todo) == 0 {
		return out, nil
	}
	c := *cc.Checker
	c.Options.Dir = filepath.Join(cc.Checker.Options.Dir, fmt.Sprintf("batch%d", cc.batches.Add(1)))
	c.Options.NoITF = true
	reps, errs := c.CheckBatch(ctx, todo, 0)
	cc.n.Add(int64(min(len(todo), runtime.GOMAXPROCS(0))))
	for j, i := range idx {
		if errs[j] != nil {
			return out, fmt.Errorf("run %d: %w", i, errs[j])
		}
		out[i] = cc.store(ks[i], reps[j])
	}
	return out, nil
}

// constants renders the run's instance constants (they may depend on the
// whole run) and says whether prefix reuse is sound for it.
func (cc *caseChecker) constants(steps []qtrace.Step) (string, bool) {
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
func (cc *caseChecker) effective(steps []qtrace.Step) ([]qtrace.Step, []int) {
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
	var k qtrace.Keyer
	for i := range steps {
		b.WriteString(k.Step(&steps[i]))
		keys = append(keys, b.String())
	}
	return keys
}

// checkCase checks a test case's recorded run and fails the test case
// (tc.Errorf) if the model rejects it, so Hegel shrinks the run against the
// model: what Test does in Each mode.
func checkCase(tc hegel.TestCase, cc *caseChecker, steps []qtrace.Step) {
	v, err := cc.Check(context.Background(), steps)
	if err != nil {
		tc.Errorf("quinthegel: checking the run: %v", err)
		return
	}
	if !v.OK {
		tc.Errorf("quinthegel: the run does not satisfy the model:\n%s", v.Summary)
	}
}
