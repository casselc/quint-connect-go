package qenv_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/casselc/quint-connect-go/qenv"
)

var errUnavailable = errors.New("unavailable")

// A point's options: nominal, its errors, then the crash of its process,
// which is restarted. Coverage counts them from the recorded choices and
// names the declared point no run reached.
func TestFaultPoints(t *testing.T) {
	qenv.DeclareFaultSite("test.write", "qenv_test.write")
	qenv.DeclareFaultSite("test.never", "qenv_test.never")
	var runs [][]qenv.Choice
	var failed, crashed int
	for seed := uint64(1); seed <= 20; seed++ {
		seeded := qenv.NewSeeded(seed)
		seeded.Rates = qenv.Rates{Default: 0.5}
		rec := &qenv.ChoiceLog{C: seeded}
		s := qenv.New(rec)
		var p *qenv.Process
		p = s.Process("writer", false, func(ctx context.Context) {
			if p != nil && p.Incarnation > 1 {
				return // restarted
			}
			for range 5 {
				switch err := qenv.FaultPoint(ctx, "test.write", true, errUnavailable); {
				case errors.Is(err, errUnavailable):
					failed++
				case err != nil:
					t.Errorf("unexpected %v", err)
				}
				qenv.Sleep(ctx, time.Second)
			}
		})
		if err := s.Run(time.Minute); err != nil {
			t.Fatal(err)
		}
		if p.Incarnation > 1 {
			crashed++
		}
		s.Close()
		runs = append(runs, rec.Choices)
	}
	if failed == 0 || crashed == 0 {
		t.Fatalf("%d errors, %d crashes injected", failed, crashed)
	}
	cov := qenv.CoverageOf(runs)
	w := cov.Point("test.write")
	if w == nil || w.Runs != 20 || len(w.Injected) != 3 || w.Injected[1] != failed || w.Injected[2] != crashed {
		t.Errorf("coverage of test.write %+v (errors %d, crashes %d)", w, failed, crashed)
	}
	if u := cov.Unreached(); len(u) != 1 || u[0].Kind != "test.never" {
		t.Errorf("unreached %v\n%s", u, cov)
	}
	t.Log(cov)

	// outside a simulation a point does nothing
	if err := qenv.FaultPoint(context.Background(), "test.write", true, errUnavailable); err != nil {
		t.Error(err)
	}
}

// Pairs count the runs that faulted two kinds together, by which came
// first; Untried names the pairs no run combined.
func TestFaultPairs(t *testing.T) {
	ch := func(kind string, v int) qenv.Choice { return qenv.Choice{Kind: "fault." + kind, N: 2, Value: v} }
	cov := qenv.CoverageOf([][]qenv.Choice{
		{ch("a", 1), ch("b", 0), ch("b", 1)},
		{ch("b", 1), ch("a", 1), ch("a", 1)},
		{ch("c", 1), ch("a", 0)},
	})
	got := map[string]qenv.PairCoverage{}
	for _, p := range cov.Pairs() {
		got[p.A+p.B] = p
	}
	if p := got["ab"]; p.Together != 2 || p.AFirst != 1 || p.BFirst != 1 {
		t.Errorf("a+b: %+v", p)
	}
	if u := cov.Untried(); len(u) != 2 || u[0].A+u[0].B != "ac" || u[1].A+u[1].B != "bc" {
		t.Errorf("untried %+v", u)
	}
	if len(cov.OneWay()) != 0 {
		t.Errorf("one way %+v", cov.OneWay())
	}
}

// Without turns kinds off and keeps the run's context where New finds it.
func TestWithout(t *testing.T) {
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "run")
	all := qenv.NewSeeded(1)
	all.Rates = qenv.Rates{Default: 1}
	c := qenv.Without(qenv.WithContext(all, ctx), "fault.test.off")
	if qenv.ContextOf(c).Value(key{}) != "run" {
		t.Fatal("the context is lost")
	}
	if c.Choose("fault.test.off.x", 2) != 0 || c.Choose("fault.test.on", 2) == 0 {
		t.Error("the wrong kinds are off")
	}
}

// counting is a user's chooser wrapper: it implements Unwrap and nothing
// else, and the run's context, entropy and nemeses by name still reach the
// simulation through it.
type counting struct {
	qenv.Chooser
	n int
}

func (c *counting) Choose(kind string, n int) int { c.n++; return c.Chooser.Choose(kind, n) }
func (c *counting) Unwrap() qenv.Chooser          { return c.Chooser }

func TestWrappersSeeThrough(t *testing.T) {
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "run")
	w := &counting{Chooser: qenv.WithContext(qenv.NewSeeded(1), ctx)}
	s := qenv.New(w)
	var seen any
	s.Process("p", false, func(ctx context.Context) { seen = ctx.Value(key{}) })
	if err := s.Run(time.Second); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if seen != "run" {
		t.Error("the context does not reach the tasks through a user's wrapper")
	}
	if qenv.EntropyOf(w) == nil {
		t.Error("the seeded chooser's entropy is hidden by the wrapper")
	}
	sc := &qenv.Script{Base: qenv.WithContext(qenv.Nominal{}, ctx)}
	if qenv.ContextOf(sc).Value(key{}) != "run" {
		t.Error("a Script loses its Base's context")
	}
	if r, ok := qenv.As[*qenv.Seeded](w); !ok || r == nil {
		t.Error("As does not find the seeded chooser")
	}
}
