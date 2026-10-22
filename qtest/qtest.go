// Package qtest records a scenario's model steps and checks them against a
// Quint model inside a Go test: no OTel, no files to manage.
//
//	func TestWithdraw(t *testing.T) {
//		qtest.Record(t, "binding.yaml", func(rec *qobs.Recorder) {
//			acct.Withdraw(ctx, 20) // woven //quint:action steps go to rec too
//			rec.Record(ctx, "deposit", nil, "amount", 5, "obs.balance", 35)
//		}).Expect(t, qtest.Conforms())
//	}
//
// The recorder is installed as the global one for the scenario's duration,
// so annotated code built with `orchestrion go test` records into it.
// Every run leaves a native step log, a model-level ITF per actor, and the
// generated Quint modules in the run's directory (t.TempDir(), or
// $QUINTGO_KEEP/<test name> to keep them).
package qtest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/casselc/quint-connect-go/qcover"
	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qobs"
	"github.com/casselc/quint-connect-go/qrun"
	"github.com/casselc/quint-connect-go/qtrace"
	"github.com/casselc/quint-connect-go/validate"
)

// Config of a recorded run.
type Config struct {
	Options     validate.Options // quint binary, backend (default $QUINTGO_BACKEND, else rust)
	Dir         string           // artifacts; default $QUINTGO_KEEP/<test> or t.TempDir()
	Recorder    []qobs.Option    // e.g. qobs.WithActor("acct-1")
	SkipIfEmpty bool             // skip (not fail) when nothing was recorded, e.g. an unwoven build
	// Constants override the binding's (validate.Checker.WithConstants).
	Constants map[string]string
	// Invariants, if set, replace the binding's list
	// (validate.Checker.WithInvariants); empty and non-nil checks none.
	Invariants []string
}

// Option changes a Config.
type Option func(*Config)

func WithOptions(o validate.Options) Option { return func(c *Config) { c.Options = o } }
func KeepIn(dir string) Option              { return func(c *Config) { c.Dir = dir } }
func RecorderOptions(opts ...qobs.Option) Option {
	return func(c *Config) { c.Recorder = append(c.Recorder, opts...) }
}
func SkipIfEmpty() Option { return func(c *Config) { c.SkipIfEmpty = true } }

// Constants checks against another instance of the binding's model: these
// constants override the binding's, e.g. Constants(map[string]string{"STOCK": "1000"}).
func Constants(m map[string]string) Option {
	return func(c *Config) {
		if c.Constants == nil {
			c.Constants = map[string]string{}
		}
		for k, v := range m {
			c.Constants[k] = v
		}
	}
}

// Invariants checks these of the model's invariants instead of the
// binding's list: none with Invariants(), for conformance alone, or a
// design-level property the binding leaves out.
func Invariants(names ...string) Option {
	return func(c *Config) { c.Invariants = append([]string{}, names...) }
}

// checkerFor is the cached checker for a binding, with c's constants and
// invariants.
func checkerFor(t testing.TB, bindingPath string, o validate.Options, c Config) *validate.Checker {
	t.Helper()
	ch := checker(t, bindingPath, o)
	var err error
	if len(c.Constants) > 0 {
		if ch, err = ch.WithConstants(c.Constants); err != nil {
			t.Fatalf("quintgo: %v", err)
		}
	}
	if c.Invariants != nil {
		if ch, err = ch.WithInvariants(c.Invariants...); err != nil {
			t.Fatalf("quintgo: %v", err)
		}
	}
	return ch
}

// Run is one checked run, whichever workflow made it: Record's, one seed of
// an Exploration, a Hegel Violation (embedded). A field a workflow has no
// value for is zero (Record makes no choices; an exploration writes no
// step log).
type Run struct {
	Seed    uint64 // an exploration's seed
	Steps   []qtrace.Step
	Choices []qenv.Choice // every choice of the run: qenv.Replay(Choices) repeats it
	// Interrupted are steps of work a crash or the end of the run cut off
	// (qobs.Recorder.Interrupted): not in Steps, not checked; reported with
	// a failing run.
	Interrupted []qtrace.Step
	// Unscripted are the faults of a Script chooser the run never came to
	// (qenv.Script.Remaining): it did not follow its script.
	Unscripted []qenv.Scripted
	// Code is the run's code coverage, with ExploreConfig.Coverage.
	Code *qcover.Counts
	// Err is the scenario's error: the run did not run to its end
	// (System.Run's error).
	Err     error
	Report  *validate.Report
	StepLog string            // Record's native step log (JSONL)
	ITF     map[string]string // Record's: actor -> model-level ITF written in-process
}

// Faults are the run's fault choices that were not nominal.
func (r *Run) Faults() []qenv.Choice { return (&qenv.Recorder{Choices: r.Choices}).Faults() }

// OK reports whether every actor conformed and satisfied the invariants.
func (r *Run) OK() bool { return r.Report != nil && r.Report.OK() }

var checkers sync.Map // binding path + options -> *validate.Checker

func checker(t testing.TB, bindingPath string, o validate.Options) *validate.Checker {
	t.Helper()
	abs, _ := filepath.Abs(bindingPath)
	key := fmt.Sprintf("%s|%s|%s", abs, o.Quint, o.Backend)
	if c, ok := checkers.Load(key); ok {
		return c.(*validate.Checker)
	}
	c, err := validate.NewChecker(abs, o) // typechecks the model and lints the binding once
	if err != nil {
		t.Fatalf("quintgo: %v", err)
	}
	checkers.Store(key, c)
	return c
}

func config(t testing.TB, opts []Option) Config {
	var c Config
	for _, o := range opts {
		o(&c)
	}
	if c.Dir == "" {
		if keep := os.Getenv("QUINTGO_KEEP"); keep != "" {
			c.Dir = filepath.Join(keep, strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()))
		} else {
			c.Dir = t.TempDir()
		}
	}
	return c
}

// Record runs scenario with a fresh recorder (also installed as the global
// one), then validates what it recorded. It does not fail the test on a
// violation: inspect the returned Run. It fails on tool errors.
func Record(t testing.TB, bindingPath string, scenario func(rec *qobs.Recorder), opts ...Option) *Run {
	t.Helper()
	c := config(t, opts)
	if err := os.MkdirAll(c.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	run := &Run{StepLog: filepath.Join(c.Dir, "steps.jsonl"), ITF: map[string]string{}}
	file, err := qobs.FileSink(run.StepLog)
	if err != nil {
		t.Fatal(err)
	}
	mem := &qobs.MemorySink{}
	rec := qobs.NewRecorder(qobs.Tee{mem, file}, c.Recorder...)
	// The scenario's goroutine is the run's (qrun.Start), whatever global
	// recorder a test in parallel installs; the global one is for the
	// goroutines it starts (servers, clusters), which no run can follow:
	// tests whose scenarios start goroutines must not run in parallel.
	end := qrun.Start(qobs.WithRecorder(context.Background(), rec))
	prev := qobs.Global()
	qobs.SetGlobal(rec)
	func() {
		defer end()
		defer qobs.SetGlobal(prev)
		scenario(rec)
	}()
	if err := file.Close(); err != nil {
		t.Fatalf("quintgo: step log: %v", err)
	}
	run.Steps = mem.Steps()
	if len(run.Steps) == 0 {
		if c.SkipIfEmpty {
			t.Skip("quintgo: no steps recorded (build with `orchestrion go test` to weave //quint:action)")
		}
		t.Fatal("quintgo: the scenario recorded no steps")
	}
	return check(t, bindingPath, run, c)
}

// RecordAndCheck is Record, failing the test (with the offending step and
// invariant) unless every actor conforms and satisfies the invariants.
//
// Deprecated: use Record(...).Expect(t, Conforms()), which states other
// verdicts the same way.
func RecordAndCheck(t testing.TB, bindingPath string, scenario func(rec *qobs.Recorder), opts ...Option) *Run {
	t.Helper()
	r := Record(t, bindingPath, scenario, opts...)
	fail(t, r)
	return r
}

// Check validates steps recorded elsewhere, failing the test on a violation.
//
// Deprecated: use Validate(...).Expect(t, Conforms()).
func Check(t testing.TB, bindingPath string, steps []qtrace.Step, opts ...Option) *Run {
	t.Helper()
	r := Validate(t, bindingPath, steps, opts...)
	fail(t, r)
	return r
}

// Validate is Check without failing the test on a violation: inspect the
// returned Run, or state the expected verdict with Run.Expect.
func Validate(t testing.TB, bindingPath string, steps []qtrace.Step, opts ...Option) *Run {
	t.Helper()
	c := config(t, opts)
	if err := os.MkdirAll(c.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	run := &Run{Steps: steps, StepLog: filepath.Join(c.Dir, "steps.jsonl"), ITF: map[string]string{}}
	f, err := os.Create(run.StepLog)
	if err == nil {
		err = qtrace.WriteJSONL(f, steps)
		f.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	return check(t, bindingPath, run, c)
}

func check(t testing.TB, bindingPath string, run *Run, c Config) *Run {
	t.Helper()
	o := c.Options
	o.Dir = filepath.Join(c.Dir, "quint")
	ch := checkerFor(t, bindingPath, o, c)
	// The model-level trace, emitted in-process from the same steps.
	if itfs, _, err := ch.Binding.StepsToITF(run.Steps); err == nil {
		for actor, data := range itfs {
			p := filepath.Join(c.Dir, strings.NewReplacer("/", "_", " ", "_").Replace(actor)+".itf.json")
			if os.WriteFile(p, data, 0o644) == nil {
				run.ITF[actor] = p
			}
		}
	} else {
		t.Logf("quintgo: no model-level ITF: %v", err)
	}
	cc := *ch // per run: its own output directory
	cc.Options.Dir = o.Dir
	rep, err := cc.Check(context.Background(), run.Steps)
	if err != nil {
		t.Fatalf("quintgo: %v", err)
	}
	run.Report = rep
	return run
}

func fail(t testing.TB, r *Run) {
	t.Helper()
	if r.OK() {
		return
	}
	var itfs []string
	for _, p := range r.ITF {
		itfs = append(itfs, p)
	}
	t.Fatalf("quintgo: the recorded trace does not satisfy the model\n%s  step log: %s\n  model-level ITF: %s",
		r.Report.Summary(), r.StepLog, strings.Join(itfs, ", "))
}
