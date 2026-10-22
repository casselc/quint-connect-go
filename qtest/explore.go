package qtest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
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
	// Err is the scenario's error: the run did not run to its end
	// (System.Run's error).
	Err error
	// Unscripted are the faults of a Script chooser the run never came to
	// (qenv.Script.Remaining): it did not follow its script.
	Unscripted []qenv.Scripted
}

// Faults are the run's fault choices that were not nominal.
func (e *Explored) Faults() []qenv.Choice { return (&qenv.Recorder{Choices: e.Choices}).Faults() }

// An Exploration is many seeded runs of a simulated scenario, checked
// against the model in bulk.
type Exploration struct {
	Runs []*Explored

	constants map[string]string // the instance checked against, for messages
	test      string            // the test's name, for reproducing a seed
	actions   map[string]bool   // the model's, for checking claims' names
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
	// Seeds is the number of runs, with seeds 1..Seeds (default 50).
	// QUINTGO_SEED=n in the environment runs seed n alone, as a failure's
	// report says to reproduce it.
	Seeds int
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
	// Chooser, if set, makes each seed's chooser instead of a seeded one
	// at Rates: a Script per seed, a Replay of a recorded run, a swarm.
	Chooser func(seed uint64) qenv.Chooser
	// Verdicts, if set, reuses the verdicts of runs with the same steps.
	Verdicts *VerdictCache
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
	return explore(t, bindingPath, cfg, func(c qenv.Chooser) error { scenario(c); return nil })
}

// explore is Explore with a scenario that returns its error: a run that
// fails to run (rather than to conform) fails t, naming its seed.
func explore(t testing.TB, bindingPath string, cfg ExploreConfig, scenario func(c qenv.Chooser) error) *Exploration {
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
	seeds := make([]uint64, cfg.Seeds)
	for i := range seeds {
		seeds[i] = uint64(i + 1)
	}
	if v := os.Getenv("QUINTGO_SEED"); v != "" {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			t.Fatalf("quintgo: QUINTGO_SEED=%q: want a seed number", v)
		}
		seeds = []uint64{n}
	}
	x := &Exploration{Runs: make([]*Explored, len(seeds)), test: t.Name()}
	runs := make([][]qtrace.Step, len(seeds))
	var wg sync.WaitGroup
	sem := make(chan struct{}, cfg.Parallel)
	for i, seed := range seeds {
		wg.Add(1)
		sem <- struct{}{}
		go func() { //qenv:ok runs seeds side by side, each its own simulation
			defer func() { <-sem; wg.Done() }()
			mem := &qobs.MemorySink{}
			var ch qenv.Chooser
			if cfg.Chooser != nil {
				ch = cfg.Chooser(seed)
			} else {
				s := qenv.NewSeeded(seed)
				s.Rates = cfg.Rates
				ch = s
			}
			rec := &qenv.Recorder{C: ch}
			var sink qobs.Sink = mem
			ctx := context.Background()
			var code *qcover.Counts
			if cfg.Coverage {
				code = qcover.NewCounts()
				ctx, sink = qcover.WithCounts(ctx, code), code.Sink(mem)
			}
			r := qobs.NewRecorder(sink, c.Recorder...)
			err := scenario(qenv.WithContext(rec, qobs.WithRecorder(ctx, r)))
			x.Runs[i] = &Explored{Seed: seed, Steps: mem.Steps(), Choices: rec.Choices, Interrupted: r.Interrupted(), Code: code, Err: err}
			if sc, ok := qenv.As[*qenv.Script](ch); ok {
				x.Runs[i].Unscripted = sc.Remaining()
			}
			runs[i] = mem.Steps()
		}()
	}
	wg.Wait()
	for _, r := range x.Runs {
		if r.Err != nil {
			t.Errorf("quintgo: seed %d did not run: %v", r.Seed, r.Err)
		}
	}
	if t.Failed() {
		// a scenario failed t (t.Fatal from a run's goroutine ends only
		// that run)
		t.FailNow()
	}
	o := c.Options
	o.Dir, o.NoITF = filepath.Join(c.Dir, "quint"), true
	ch := checkerFor(t, bindingPath, o, c)
	x.constants = map[string]string{}
	for k, v := range ch.Binding.Constants {
		x.constants[k] = v
	}
	for k, v := range c.Constants {
		x.constants[k] = v
	}
	if ch.Model != nil {
		x.actions = map[string]bool{}
		for a := range ch.Model.Actions {
			x.actions[a] = true
		}
	}
	// a verdict is of steps against one instance: the binding, its
	// constants (a sweep's rows set their own) and the invariants checked
	var inst strings.Builder
	fmt.Fprintf(&inst, "%s|%v|%v\n", bindingPath, sortedKV(x.constants), c.Invariants)
	cc := *ch
	cc.Options = o
	// runs already judged (same steps) take their verdict from the cache,
	// and runs of the batch with the same steps are checked once
	keys := make([]string, len(runs))
	var todo [][]qtrace.Step
	first := map[string]int{} // key -> index in todo
	at := map[int]int{}       // run -> index in todo
	for i, steps := range runs {
		if cfg.Verdicts != nil {
			keys[i] = inst.String() + stepsKey(steps)
			if rep, ok := cfg.Verdicts.get(keys[i]); ok {
				x.Runs[i].Report = rep
				continue
			}
			if j, ok := first[keys[i]]; ok {
				at[i] = j
				cfg.Verdicts.hit()
				continue
			}
			first[keys[i]] = len(todo)
		}
		at[i] = len(todo)
		todo = append(todo, steps)
	}
	if len(todo) > 0 {
		reps, errs := cc.CheckBatch(context.Background(), todo, 0)
		for i, j := range at {
			if errs[j] != nil {
				t.Fatalf("quintgo: seed %d: %v", x.Runs[i].Seed, errs[j])
			}
			x.Runs[i].Report = reps[j]
			if cfg.Verdicts != nil {
				cfg.Verdicts.put(keys[i], reps[j])
			}
		}
	}
	return x
}

// A VerdictCache keeps the model's verdicts by the runs' steps: a run whose
// steps were already judged is not checked again. Fuzzing needs it (most
// inputs repeat a run's model-level steps, and quint takes a second), as
// does any exploration whose seeds repeat runs.
type VerdictCache struct {
	mu   sync.Mutex
	m    map[string]*validate.Report
	hits int
}

// Hits counts the runs judged from the cache.
func (v *VerdictCache) Hits() int { v.mu.Lock(); defer v.mu.Unlock(); return v.hits }

func (v *VerdictCache) get(k string) (*validate.Report, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	r, ok := v.m[k]
	if ok {
		v.hits++
	}
	return r, ok
}

func (v *VerdictCache) hit() { v.mu.Lock(); v.hits++; v.mu.Unlock() }

func (v *VerdictCache) put(k string, r *validate.Report) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.m == nil {
		v.m = map[string]*validate.Report{}
	}
	v.m[k] = r
}

// stepsKey is a run's steps as the model sees them: actions, outcomes,
// arguments, observations and order keys, with recorder, process and
// thread ids renumbered by first appearance (they are random per run) and
// times left out.
func stepsKey(steps []qtrace.Step) string {
	ids := map[string]int{}
	id := func(s string) int {
		if n, ok := ids[s]; ok {
			return n
		}
		ids[s] = len(ids)
		return ids[s]
	}
	var b strings.Builder
	for _, s := range steps {
		fmt.Fprintf(&b, "%s|%s|%d|%d|%d|%s|%v|%v|%v\n", s.Action, s.Outcome, id("r:"+s.Recorder), id("p:"+s.Process), id("t:"+s.Thread),
			s.Actor, sortedKV(s.Args), sortedKV(s.Obs), sortedKV(s.Order))
	}
	return b.String()
}

func sortedKV[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, fmt.Sprintf("%s=%v", k, v))
	}
	sort.Strings(out)
	return out
}

// Expect states what the exploration must show, claim by claim: Some,
// All or None of the runs meeting expectations, or an Expectation alone
// (some run; every run for Conforms). With no claims, every run conforms.
// A claim that holds by a failing run is logged with that run; one that
// does not fails t with the run that shows why (its report and faults,
// its step log saved), or, when no run fails at all, the fault kinds the
// runs never faulted.
func (x *Exploration) Expect(t testing.TB, claims ...Claim) {
	t.Helper()
	if len(claims) == 0 {
		claims = []Claim{Conforms()}
	}
	reps := make([]*validate.Report, len(x.Runs))
	invariants := map[string]bool{}
	for i, r := range x.Runs {
		reps[i] = r.Report
		for k := range reportInvariants(r.Report) {
			invariants[k] = true
		}
	}
	if len(invariants) == 0 {
		invariants = nil
	}
	for _, c := range claims {
		var es []Expectation
		switch c := c.(type) {
		case Expectation:
			es = []Expectation{c}
		case quantified:
			es = c.es
		}
		if u := unknownNames(es, x.actions, invariants); len(u) > 0 {
			t.Fatalf("quintgo: %s names %s: a misspelled name never matches, so the claim would hold or fail for nothing", describeClaim(c), strings.Join(u, "; "))
		}
	}
	bad := len(x.Failing())
	failedAny := false
	for _, c := range claims {
		j := c.judge(reps)
		q, isQ := c.(quantified)
		every := !isQ && c.(Expectation).every || isQ && q.kind == "all"
		switch {
		case j.ok && j.show >= 0 && !every:
			r := x.Runs[j.show]
			if !r.Report.OK() {
				t.Logf("quintgo: %d of %d runs violate the model; seed %d shows %s:\n%s", bad, len(x.Runs), r.Seed, c, x.describe(t, r))
			}
		case j.ok:
		case j.show >= 0:
			failedAny = true
			what := "show"
			if every {
				what = "do not show"
			}
			t.Errorf("quintgo: %d of %d runs %s %s%s; the first:\n%s", j.n, len(x.Runs), what, describeClaim(c), x.atStart(), x.describe(t, x.Runs[j.show]))
		case bad == 0:
			failedAny = true
			t.Errorf("quintgo: all %d runs conform; want %s%s%s", len(x.Runs), c, x.unfaulted(), x.unscripted())
		default:
			failedAny = true
			first := x.Failing()[0]
			t.Errorf("quintgo: %d of %d runs violate the model, none as %s%s; the first:\n%s", bad, len(x.Runs), c, x.atStart(), x.describe(t, first))
		}
	}
	if failedAny {
		t.FailNow()
	}
}

// describeClaim names what a failing All or None was about.
func describeClaim(c Claim) string {
	if q, ok := c.(quantified); ok {
		var d []string
		for _, e := range q.es {
			d = append(d, e.desc)
		}
		return strings.Join(d, " and ")
	}
	return c.String()
}

// unfaulted names the fault kinds no run faulted: a reason a bug did not
// show.
func (x *Exploration) unfaulted() string {
	var kinds []string
	for _, p := range x.Coverage().Unfaulted() {
		kinds = append(kinds, p.Kind)
	}
	if len(kinds) == 0 {
		return ""
	}
	return fmt.Sprintf(" (never faulted: %s; raise their rates or the seeds)", strings.Join(kinds, ", "))
}

// atStart says when every run fails at its first step: the model's
// instance (its constants) more likely disagrees with the scenario's than
// every run has the same bug.
func (x *Exploration) atStart() string {
	if len(x.Runs) < 2 {
		return ""
	}
	for _, r := range x.Runs {
		if r.Report.OK() || !failsAtStart(r.Report) {
			return ""
		}
	}
	var cs []string
	for _, k := range keysOf(x.constants) {
		cs = append(cs, k+"="+x.constants[k])
	}
	return fmt.Sprintf(" (all %d runs fail at step 1: the binding's constants (%s) may not match the scenario's; pass qtest.Constants)", len(x.Runs), strings.Join(cs, ", "))
}

func failsAtStart(rep *validate.Report) bool {
	for _, a := range rep.Actors {
		if a.Failure != nil && a.Failure.Index == 0 {
			return true
		}
	}
	return false
}

// unscripted names the scripted faults runs never came to: a reason a
// scripted bug did not show.
func (x *Exploration) unscripted() string {
	n := 0
	var first []qenv.Scripted
	for _, r := range x.Runs {
		if len(r.Unscripted) > 0 {
			if n == 0 {
				first = r.Unscripted
			}
			n++
		}
	}
	if n == 0 {
		return ""
	}
	return fmt.Sprintf(" (in %d of %d runs scripted faults never happened, the first's: %v; a fault of a phase that has passed blocks every later one)", n, len(x.Runs), first)
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
	if len(r.Steps) == 0 {
		fmt.Fprintf(&b, "  the run recorded no steps: its tasks never reached a step (a deadlock, a wait no one ends: print Sim.Tasks() before Close), or it records into another recorder\n")
	}
	if len(r.Unscripted) > 0 {
		fmt.Fprintf(&b, "  scripted faults that never happened: %v\n", r.Unscripted)
	}
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
	if x.test != "" {
		fmt.Fprintf(&b, "  reproduce: QUINTGO_SEED=%d go test -run '^%s$'\n", r.Seed, strings.ReplaceAll(x.test, "/", "$/^"))
	}
	return b.String()
}
