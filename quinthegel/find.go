package quinthegel

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/casselc/quint-connect-go/qtest"
	"github.com/casselc/quint-connect-go/qtrace"
	"hegel.dev/go/hegel"
)

// A Violation is the run FindViolation found, shrunk: the steps of the
// smallest failing test case and the model's verdict on them. Its Run is
// an exploration's run's record: Report is its full verdict, for Expect
// (checked once more, since cached verdicts keep only their summary), and
// Choices, if it ran on Recorder.Env, replay it outside Hegel
// (qenv.Replay(v.Choices): a regression test, a run on gosim, a fuzz
// seed).
type Violation struct {
	qtest.Run
	Summary string // the validator's report on Steps
	// QuintRuns and CacheHits count the cases checked with quint and those
	// decided from earlier verdicts (see caseChecker).
	QuintRuns, CacheHits int64

	found    bool // a case violated the model
	expected bool // Expect was called
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
// When no case violates the model, the Violation is empty: Expect(t,
// qtest.Conforms()) says that is what the test wants (Hegel finds
// nothing), any other expectation fails, and without Expect the test
// fails at its end.
//
// cfg.Mode is ignored. hegel.Test cannot express an expected failure, so
// this uses hegel.Run, with no example database (a later WithDatabase in
// opts wins) and Hegel's "too slow" health check off: every case runs quint.
// Hegel runs quiet, without the reproduction blob of the failure it is
// expected to find (QUINTGO_VERBOSE=1 shows both); the shrunk run is
// logged.
func FindViolation(t testing.TB, cfg Config, body func(tc hegel.TestCase, rec *Recorder), opts ...hegel.Option) *Violation {
	t.Helper()
	vo := cfg.Validate
	vo.Dir = filepath.Join(keepDir(t, cfg.Keep), "quint")
	vo.NoITF = true
	c, err := cfg.checker(vo)
	if err != nil {
		t.Fatalf("quinthegel: %v", err)
	}
	cc := newCaseChecker(c)
	var (
		mu    sync.Mutex
		found *Violation
	)
	base := []hegel.Option{hegel.SuppressHealthCheck(hegel.TooSlow), hegel.WithDatabase("")}
	quiet := os.Getenv("QUINTGO_VERBOSE") != "1"
	if quiet {
		base = append(base, hegel.WithVerbosity(hegel.VerbosityQuiet), hegel.WithReproductionBlob(false))
	}
	opts = append(base, opts...)
	var hegelOut *captured
	if quiet {
		// Hegel replays the failure it is expected to find on os.Stdout
		// (draws, notes, the failing case's stack): captured, and shown
		// only when the outcome is not the expected one
		hegelOut = captureStdout()
	}
	var out string
	func() {
		defer func() { out = hegelOut.stop() }() // also when a case's t.Fatal ends the goroutine
		err = hegel.Run(func(tc hegel.TestCase) {
			rec := NewRecorder(tc, cfg.Recorder...)
			defer rec.begin(tc, cfg.Global)()
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
				found = &Violation{Run: qtest.Run{Steps: steps, Choices: rec.Choices()}, Summary: v.Summary}
				mu.Unlock()
				tc.Errorf("quinthegel: the run does not satisfy the model:\n%s", v.Summary)
			}
		}, opts...)
	}()
	if found == nil && err != nil && out != "" {
		t.Logf("quinthegel: Hegel's output:\n%s", out)
	}
	q, hits := cc.Stats()
	switch {
	case found == nil && err == nil:
		v := &Violation{QuintRuns: q, CacheHits: hits}
		t.Cleanup(func() {
			if !v.expected {
				t.Errorf("quinthegel: every test case satisfied the model (%d checked with quint); want a violation, "+
					"or say none is wanted with Expect(t, qtest.Conforms())", q)
			}
		})
		t.Logf("quinthegel: no case violated the model (%d checked with quint, %d decided from earlier verdicts)", q, hits)
		return v
	case found == nil:
		t.Fatalf("quinthegel: no case violated the model, but the test failed: %v", err)
	}
	found.found = true
	found.QuintRuns, found.CacheHits = q, hits
	if found.Report, err = c.Check(context.Background(), found.Steps); err != nil {
		t.Fatalf("quinthegel: checking the shrunk run: %v", err)
	}
	faults := ""
	if len(found.Choices) > 0 {
		var fs []string
		for _, c := range found.Faults() {
			fs = append(fs, c.String())
		}
		faults = "faults: none\n"
		if len(fs) > 0 {
			faults = "faults: " + strings.Join(fs, ", ") + "\n"
		}
		faults += "replay it outside Hegel: qenv.Replay(v.Choices)\n"
	}
	t.Logf("quinthegel: %d runs checked with quint, %d decided from earlier verdicts; shrunk run:\n%s%s%s",
		q, hits, Render(found.Steps), faults, found.Summary)
	return found
}

// Expect fails t unless the shrunk run's verdict meets every expectation
// (qtest's Rejects, Mismatch, Violates, AnyOf): what the failure Hegel
// found must look like, stated as for an exploration.
//
// Expect(t, qtest.Conforms()) wants no violation: Hegel found nothing.
func (v *Violation) Expect(t testing.TB, es ...qtest.Expectation) {
	t.Helper()
	v.expected = true
	if !v.found {
		for _, e := range es {
			if e.String() != qtest.Conforms().String() {
				t.Fatalf("quinthegel: every test case satisfied the model (%d checked with quint); want %s", v.QuintRuns, e)
			}
		}
		return
	}
	v.Run.Expect(t, es...)
}

// Last returns the last step of the run, or the zero step if it is empty.
func (v *Violation) Last() qtrace.Step {
	if len(v.Steps) == 0 {
		return qtrace.Step{}
	}
	return v.Steps[len(v.Steps)-1]
}

// Shrink runs Hegel against the system, expecting the model to reject
// some test case, and shrinks it (FindViolation): every choice of the
// system's environment is Hegel's (Chooser), recorded with the run's steps
// (Recorder.Env), so the shrunk Violation replays outside Hegel.
//
//	quinthegel.Shrink(t, sys, hegel.WithTestCases(50)).Expect(t, qtest.Rejects("ship"))
func Shrink(t testing.TB, sys qtest.System, opts ...hegel.Option) *Violation {
	t.Helper()
	return FindViolation(t, Config{Binding: sys.Binding, Constants: sys.Constants}, func(tc hegel.TestCase, rec *Recorder) {
		if err := sys.Run(rec.Env(Chooser(tc))); err != nil {
			tc.Errorf("quinthegel: the system did not run: %v", err)
		}
	}, opts...)
}

// stdoutMu serializes the redirections of os.Stdout: one Hegel run's at a
// time.
var stdoutMu sync.Mutex

// captured is os.Stdout redirected into a buffer until stop.
type captured struct {
	old, w *os.File
	done   chan string
}

// captureStdout redirects os.Stdout into a buffer (nil if it cannot).
// Output of other tests in parallel lands there too, which is the price
// of a quiet expected failure: Hegel writes to os.Stdout and nowhere else.
func captureStdout() *captured {
	stdoutMu.Lock()
	r, w, err := os.Pipe()
	if err != nil {
		stdoutMu.Unlock()
		return nil
	}
	c := &captured{old: os.Stdout, w: w, done: make(chan string)}
	go func() { // drains the pipe so writers never block
		var b strings.Builder
		_, _ = io.Copy(&b, r)
		r.Close()
		c.done <- b.String()
	}()
	os.Stdout = w
	return c
}

// stop restores os.Stdout and returns what was written.
func (c *captured) stop() string {
	if c == nil {
		return ""
	}
	os.Stdout = c.old
	c.w.Close()
	out := <-c.done
	stdoutMu.Unlock()
	return out
}
