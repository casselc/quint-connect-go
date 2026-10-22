package qtest

import (
	"context"
	"testing"

	"github.com/casselc/quint-connect-go/connect"
	"github.com/casselc/quint-connect-go/itf"
	"github.com/casselc/quint-connect-go/qenv"
)

// A System is an application bound to its model: the binding, the options
// of its checks (the model's constants for its instance, ...), and how to
// run it on a chooser. The
// workflows over it share that one definition: explore seeds (Explore),
// follow a model's traces through their faults (FollowTraces), sweep
// configurations (Sweep), and shrink a failure with Hegel
// (quinthegel.Shrink).
//
//	sys := qtest.System{Binding: "checkout.binding.yaml", Run: func(c qenv.Chooser) error {
//		_, err := checkout.Run(c, cfg)
//		return err
//	}}
//	sys.Explore(t, qtest.ExploreConfig{Seeds: 100}).Expect(t, qtest.Conforms())
type System struct {
	Binding string
	// Opts apply to every workflow over the system: the model's constants
	// for this instance (Constants), the invariants checked (Invariants),
	// quint's options (Quint), the recorder's (RecorderOptions).
	Opts []Option
	// Run deploys and runs the application on c: a simulation made from c
	// (qenv.New(c)) records into the run's recorder. An error means the run
	// could not run to its end, not that it violates the model: it fails
	// the test, naming its seed.
	Run func(c qenv.Chooser) error
}

func (s System) opts(more ...Option) []Option {
	return append(append([]Option(nil), s.Opts...), more...)
}

// Explore runs the system once per seed, each time with a seeded qenv
// chooser (wrapped in a qenv.Recorder, so every run can be replayed) whose
// context carries a fresh step recorder, then checks every run in one
// batch (validate.Checker.CheckBatch, without ITF). It is how a simulated
// system is explored like Maelstrom or Jepsen explore real ones: many
// random schedules and faults, every one judged by the model. State
// expectations with Exploration.Expect.
func (s System) Explore(t testing.TB, cfg ExploreConfig) *Exploration {
	t.Helper()
	cfg.Opts = s.opts(cfg.Opts...)
	return explore(t, s.Binding, cfg, s.Run)
}

// FollowTraces drives the whole system through each of the model's traces'
// faults (connect.FaultScript): the trace's mapped actions are imposed as
// faults in its order, the system makes every other step itself, and its
// recorded run is checked against the model. A run that cannot follow its
// trace (a scripted fault it never came to) fails the test with the
// faults left. trace configures `quint run` (Dir defaults to a temporary
// directory).
func (s System) FollowTraces(t testing.TB, trace connect.Config, faults connect.FaultMap) *Exploration {
	t.Helper()
	if trace.Dir == "" {
		trace.Dir = t.TempDir()
	}
	files, _, err := connect.GenerateRun(context.Background(), trace)
	if err != nil {
		t.Fatal(err)
	}
	scripts := make([]*qenv.Script, len(files))
	for i, f := range files {
		tr, err := itf.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if scripts[i], err = connect.FaultScript(tr, faults, connect.ReplayOptions{Trace: i, StatePath: trace.StatePath, NondetPath: trace.NondetPath}); err != nil {
			t.Fatal(err)
		}
	}
	x := s.Explore(t, ExploreConfig{Seeds: len(files), Chooser: func(seed uint64) qenv.Chooser { return scripts[seed-1] }})
	for i, sc := range scripts {
		if r := sc.Remaining(); len(r) > 0 {
			t.Errorf("quintgo: trace %d: scripted faults never happened: %v (the run did not follow its trace: "+
				"the fault's kind never came up after the steps before it)", i, r)
		}
	}
	if t.Failed() {
		t.FailNow()
	}
	return x
}

// Sweep explores each covering row of sw as a subtest named by the row,
// checking its expectation there. The system's Run reads the row from its
// chooser (RowOf) and configures itself from it: one Run for every
// workflow. A row's FaultKind factors that are off are off in the chooser.
func (s System) Sweep(t *testing.T, sw Sweep) *Swept {
	t.Helper()
	sw.Explore.Opts = s.opts(sw.Explore.Opts...)
	return sweep(t, s.Binding, sw, s.Run)
}

// Fuzz explores the system with Go's coverage-guided fuzzer: each input is
// one run, its bytes the run's choices (qenv.FromBytes at rates:
// schedules, faults; zero rates are qenv.DefaultRates), the model the
// oracle. Without -fuzz, go test runs the seeds and the corpus in
// testdata/fuzz as regression tests. Seed it with runs worth keeping: a
// shrunk Hegel failure, once fixed, is qenv.EncodeChoices(v.Choices,
// rates). The empty input, every choice nominal, is always a seed. Inputs
// that repeat a run's steps take its verdict without quint.
//
//	func FuzzShop(f *testing.F) { sys.Fuzz(f, rates) }
func (s System) Fuzz(f *testing.F, rates qenv.Rates, seeds ...[]byte) {
	f.Helper()
	f.Add([]byte{})
	for _, sd := range seeds {
		f.Add(sd)
	}
	if rates.Default == 0 && rates.Kinds == nil {
		rates = qenv.DefaultRates
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		s.Explore(t, ExploreConfig{Seeds: 1, Parallel: 1,
			Chooser: func(uint64) qenv.Chooser { return qenv.FromBytes(data, rates) }}).Expect(t, Conforms())
	})
}
