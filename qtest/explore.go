package qtest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/casselc/quint-connect-go/qcover"
	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qobs"
	"github.com/casselc/quint-connect-go/qtrace"
	"github.com/casselc/quint-connect-go/validate"
)

// Explored is one seeded run of an Exploration.
type Explored struct {
	Seed    uint64
	Steps   []qtrace.Step
	Choices []qenv.Choice // every choice of the run: qenv.Replay(Choices) repeats it
	// Interrupted are steps of work a crash or the end of the run cut off
	// (qobs.Recorder.Interrupted): not in Steps, not checked; reported with
	// a failing run.
	Interrupted []qtrace.Step
	Report      *validate.Report
	// Code is the run's code coverage, with ExploreConfig.Coverage.
	Code *qcover.Counts
}

// Faults are the run's fault choices that were not nominal.
func (e *Explored) Faults() []qenv.Choice {
	var out []qenv.Choice
	for _, c := range e.Choices {
		if qenv.IsFault(c.Kind) && c.Value != 0 {
			out = append(out, c)
		}
	}
	return out
}

// An Exploration is many seeded runs of a simulated scenario, checked
// against the model in bulk.
type Exploration struct {
	Runs []*Explored
}

// Coverage is the runs' fault coverage (qenv.CoverageOf): for each fault
// kind, declared with //quint:fault or a double's, how many runs reached it
// and which options they took. A declared point no run reached is a
// failure path the exploration never tried.
func (x *Exploration) Coverage() *qenv.Coverage {
	runs := make([][]qenv.Choice, len(x.Runs))
	for i, r := range x.Runs {
		runs[i] = r.Choices
	}
	return qenv.CoverageOf(runs)
}

// CodeCoverage is the runs' code coverage read against their steps
// (ExploreConfig.Coverage): what no run ran, what ran under no step, and
// the outcomes of an action that ran the same code.
func (x *Exploration) CodeCoverage() *qcover.Summary {
	runs := make([]*qcover.Counts, len(x.Runs))
	for i, r := range x.Runs {
		runs[i] = r.Code
	}
	return qcover.Summarize(runs)
}

// Failing returns the runs that violate the model, in seed order.
func (x *Exploration) Failing() []*Explored {
	var out []*Explored
	for _, r := range x.Runs {
		if !r.Report.OK() {
			out = append(out, r)
		}
	}
	return out
}

// ExploreConfig configures Explore.
type ExploreConfig struct {
	Seeds int        // runs, with seeds 1..Seeds (default 50)
	Rates qenv.Rates // fault rates (default qenv.DefaultRates)
	Opts  []Option   // artifacts, quint options
	// Parallel runs this many seeds at once (default GOMAXPROCS; 1: one
	// after another). Each run records into its own recorder, carried by
	// the chooser's context (qenv.WithContext), so the scenario must build
	// its simulation from the chooser it is given and keep no state across
	// runs. A step lands in its run if it is recorded through a context of
	// the run or from a goroutine of the simulation (its tasks, the one
	// that created it); one recorded from a goroutine started with `go`
	// has nowhere to go and panics (qobs.Strict).
	Parallel int
	// Coverage collects each run's code coverage, by step (qcover; the
	// code must be woven with qweave -cover): Exploration.CodeCoverage.
	Coverage bool
}

// Explore runs scenario once per seed, each time with a seeded qenv chooser
// (wrapped in a qenv.Recorder, so every run can be replayed) whose context
// carries a fresh step recorder, then checks every run in
// one batch (validate.Checker.CheckBatch, without ITF). It is how a
// simulated system is explored like Maelstrom or Jepsen explore real
// ones: many random schedules and faults, every one judged by the model.
// State expectations with Exploration.Expect.
func Explore(t testing.TB, bindingPath string, cfg ExploreConfig, scenario func(c qenv.Chooser)) *Exploration {
	t.Helper()
	if cfg.Seeds == 0 {
		cfg.Seeds = 50
	}
	if cfg.Rates.Default == 0 && cfg.Rates.Kinds == nil {
		cfg.Rates = qenv.DefaultRates
	}
	if cfg.Parallel <= 0 {
		cfg.Parallel = runtime.GOMAXPROCS(0)
	}
	c := config(t, cfg.Opts)
	defer qobs.Strict()() // a step with nowhere to go panics, rather than vanish from its run
	x := &Exploration{Runs: make([]*Explored, cfg.Seeds)}
	runs := make([][]qtrace.Step, cfg.Seeds)
	var wg sync.WaitGroup
	sem := make(chan struct{}, cfg.Parallel)
	for i := range cfg.Seeds {
		seed := uint64(i + 1)
		wg.Add(1)
		sem <- struct{}{}
		go func() { //qenv:ok runs seeds side by side, each its own simulation
			defer func() { <-sem; wg.Done() }()
			mem := &qobs.MemorySink{}
			ch := qenv.NewSeeded(seed)
			ch.Rates = cfg.Rates
			rec := &qenv.Recorder{C: ch}
			var sink qobs.Sink = mem
			ctx := context.Background()
			var code *qcover.Counts
			if cfg.Coverage {
				code = qcover.NewCounts()
				ctx, sink = qcover.WithCounts(ctx, code), code.Sink(mem)
			}
			r := qobs.NewRecorder(sink, c.Recorder...)
			scenario(qenv.WithContext(rec, qobs.WithRecorder(ctx, r)))
			x.Runs[i] = &Explored{Seed: seed, Steps: mem.Steps(), Choices: rec.Choices, Interrupted: r.Interrupted(), Code: code}
			runs[i] = mem.Steps()
		}()
	}
	wg.Wait()
	if t.Failed() {
		// a scenario failed t (t.Fatal from a run's goroutine ends only
		// that run)
		t.FailNow()
	}
	o := c.Options
	o.Dir, o.NoITF = filepath.Join(c.Dir, "quint"), true
	ch := checkerFor(t, bindingPath, o, c)
	cc := *ch
	cc.Options = o
	reps, errs := cc.CheckBatch(context.Background(), runs, 0)
	for i, r := range x.Runs {
		if errs[i] != nil {
			t.Fatalf("quintgo: seed %d: %v", r.Seed, errs[i])
		}
		r.Report = reps[i]
	}
	return x
}

// Expect states what the exploration must show. With Conforms(): every
// run conforms. Otherwise: some run fails and meets every expectation (a
// bug can show up in more than one way; this says how it can). On a miss
// it fails t with the first failing run's report and faults, and saves its
// step log.
func (x *Exploration) Expect(t testing.TB, es ...Expectation) {
	t.Helper()
	bad := x.Failing()
	conforms := len(es) == 1 && es[0].desc == Conforms().desc
	if conforms {
		if len(bad) > 0 {
			t.Fatalf("quintgo: %d of %d runs violate the model; the first:\n%s", len(bad), len(x.Runs), x.describe(t, bad[0]))
		}
		return
	}
	if len(bad) == 0 {
		t.Fatalf("quintgo: all %d runs conform; want %s", len(x.Runs), describeAll(es))
	}
	for _, r := range bad {
		ok := true
		for _, e := range es {
			ok = ok && e.check(r.Report)
		}
		if ok {
			t.Logf("quintgo: %d of %d runs violate the model; seed %d shows %s:\n%s", len(bad), len(x.Runs), r.Seed, describeAll(es), x.describe(t, r))
			return
		}
	}
	t.Fatalf("quintgo: %d of %d runs violate the model, none as %s; the first:\n%s", len(bad), len(x.Runs), describeAll(es), x.describe(t, bad[0]))
}

func describeAll(es []Expectation) string {
	var s []string
	for _, e := range es {
		s = append(s, e.desc)
	}
	return strings.Join(s, " and ")
}

func (x *Exploration) describe(t testing.TB, r *Explored) string {
	var b strings.Builder
	fmt.Fprintf(&b, "seed %d, faults %v\n%s", r.Seed, r.Faults(), r.Report.Summary())
	if len(r.Interrupted) > 0 {
		fmt.Fprintf(&b, "  interrupted, not checked (cut off by a crash or the end of the run):\n")
		for _, s := range r.Interrupted {
			fmt.Fprintf(&b, "    %s %v (process %s)\n", s.Action, s.Args, s.Process)
		}
	}
	dir := t.TempDir()
	p := filepath.Join(dir, fmt.Sprintf("seed%d.steps.jsonl", r.Seed))
	if f, err := os.Create(p); err == nil {
		if qtrace.WriteJSONL(f, r.Steps) == nil {
			fmt.Fprintf(&b, "  step log: %s\n", p)
		}
		f.Close()
	}
	return b.String()
}
