package quinthegel

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qtest"
	"github.com/casselc/quint-connect-go/qtrace"
	"github.com/casselc/quint-connect-go/validate"
	"hegel.dev/go/hegel"
)

// A Violation is the run FindViolation found, shrunk: the steps of the
// smallest failing test case and the model's verdict on them.
type Violation struct {
	Steps   []qtrace.Step
	Summary string // the validator's report on Steps
	// Report is the shrunk run's full verdict, for Expect: it is checked
	// once more, since cached verdicts keep only their summary.
	Report *validate.Report
	// Choices are the shrunk run's choices, if it ran on Recorder.Env:
	// qenv.Replay(v.Choices) repeats it outside Hegel (a regression test,
	// a run on gosim, a fuzz seed).
	Choices []qenv.Choice
	// QuintRuns and CacheHits count the cases checked with quint and those
	// decided from earlier verdicts (see CaseChecker).
	QuintRuns, CacheHits int64
}

// FindViolation runs Hegel expecting the model to reject some test case:
// a mutant, or a bug being reproduced. Every case is checked as it runs
// (Each mode, with the verdict cache), so Hegel shrinks the failure to its
// smallest run. It fails t when every case satisfies the model, and returns
// the shrunk run for the test to assert on:
//
//	v := quinthegel.FindViolation(t, quinthegel.Config{Binding: "bank.binding.yaml"},
//		func(tc hegel.TestCase, rec *quinthegel.Recorder) {
//			hegel.RunStateful(tc, &machine{bank: New(accounts, WithMutation(SelfTransfer))})
//		}, hegel.WithTestCases(50), hegel.WithSeed(3))
//
// cfg.Mode is ignored. hegel.Test cannot express an expected failure, so
// this uses hegel.Run, with no example database (a later WithDatabase in
// opts wins) and Hegel's "too slow" health check off: every case runs quint.
func FindViolation(t testing.TB, cfg Config, body func(tc hegel.TestCase, rec *Recorder), opts ...hegel.Option) *Violation {
	t.Helper()
	vo := cfg.Validate
	vo.Dir = filepath.Join(keepDir(t, cfg.Keep), "quint")
	vo.NoITF = true
	c, err := cfg.checker(vo)
	if err != nil {
		t.Fatalf("quinthegel: %v", err)
	}
	cc := NewCaseChecker(c)
	var (
		mu    sync.Mutex
		found *Violation
	)
	opts = append([]hegel.Option{hegel.SuppressHealthCheck(hegel.TooSlow), hegel.WithDatabase("")}, opts...)
	err = hegel.Run(func(tc hegel.TestCase) {
		rec := NewRecorder(tc, cfg.Recorder...)
		if cfg.Global {
			defer rec.Install()()
		}
		body(tc, rec)
		steps := rec.Steps()
		if len(steps) == 0 && cfg.AllowEmpty {
			return
		}
		v, err := cc.Check(context.Background(), steps)
		if err != nil {
			tc.Errorf("quinthegel: checking the run: %v", err)
			return
		}
		if !v.OK {
			// Hegel replays the shrunk case last, so the last failure is it
			mu.Lock()
			found = &Violation{Steps: steps, Summary: v.Summary, Choices: rec.Choices()}
			mu.Unlock()
			tc.Errorf("quinthegel: the run does not satisfy the model:\n%s", v.Summary)
		}
	}, opts...)
	q, hits := cc.Stats()
	switch {
	case found == nil && err == nil:
		t.Fatalf("quinthegel: every test case satisfied the model (%d checked with quint); want a violation", q)
	case found == nil:
		t.Fatalf("quinthegel: no case violated the model, but the test failed: %v", err)
	}
	found.QuintRuns, found.CacheHits = q, hits
	if found.Report, err = c.Check(context.Background(), found.Steps); err != nil {
		t.Fatalf("quinthegel: checking the shrunk run: %v", err)
	}
	t.Logf("quinthegel: %d runs checked with quint, %d decided from earlier verdicts; shrunk run:\n%s%s",
		q, hits, Render(found.Steps), found.Summary)
	return found
}

// Expect fails t unless the shrunk run's verdict meets every expectation
// (qtest's Rejects, Mismatch, Violates, AnyOf): what the failure Hegel
// found must look like, stated as for an exploration.
func (v *Violation) Expect(t testing.TB, es ...qtest.Expectation) {
	t.Helper()
	qtest.Expect(t, v.Report, es...)
}

// Last returns the last step of the run, or the zero step if it is empty.
func (v *Violation) Last() qtrace.Step {
	if len(v.Steps) == 0 {
		return qtrace.Step{}
	}
	return v.Steps[len(v.Steps)-1]
}
