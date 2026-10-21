package qtest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	Report  *validate.Report
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
}

// Explore runs scenario once per seed, each time with a fresh recorder
// installed as the global one and a seeded qenv chooser (wrapped in a
// qenv.Recorder, so every run can be replayed), then checks every run in
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
	c := config(t, cfg.Opts)
	x := &Exploration{}
	var runs [][]qtrace.Step
	prev := qobs.Global()
	for seed := uint64(1); seed <= uint64(cfg.Seeds); seed++ {
		mem := &qobs.MemorySink{}
		qobs.SetGlobal(qobs.NewRecorder(mem, c.Recorder...))
		ch := qenv.NewSeeded(seed)
		ch.Rates = cfg.Rates
		rec := &qenv.Recorder{C: ch}
		func() {
			defer qobs.SetGlobal(prev)
			scenario(rec)
		}()
		x.Runs = append(x.Runs, &Explored{Seed: seed, Steps: mem.Steps(), Choices: rec.Choices})
		runs = append(runs, mem.Steps())
	}
	o := c.Options
	o.Dir, o.NoITF = filepath.Join(c.Dir, "quint"), true
	ch := checker(t, bindingPath, o)
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
