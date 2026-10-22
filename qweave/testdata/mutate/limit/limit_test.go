package limit

import (
	"context"
	"testing"
	"time"

	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qmut"
)

// run admits three requests into a limiter of two, in a run with mutant
// id on (-1 for none), and says what it saw. Woven code finds its run's
// mutant through its goroutine's simulation, as it would under test.
func run(t *testing.T, id int) (admitted int, full bool, logged int, reach *qmut.Reach) {
	reach = &qmut.Reach{}
	ctx := qmut.WithReach(context.Background(), reach)
	if id >= 0 {
		ctx = qmut.WithMutant(ctx, id)
	}
	sim := qenv.New(qenv.WithContext(qenv.NewSeeded(1), ctx))
	sim.Process("app", false, func(context.Context) {
		l := &Limiter{Max: 2}
		for range 3 {
			if l.Admit() {
				admitted++
			}
		}
		full, logged = l.Full(), len(l.Log)
	})
	if err := sim.Run(time.Minute); err != nil {
		t.Fatal(err)
	}
	sim.Close()
	return
}

func TestEveryMutantChangesBehaviour(t *testing.T) {
	ms := qmut.Mutants()
	if len(ms) == 0 {
		t.Fatal("not woven: run with qweave -mutate")
	}
	ops := map[string]bool{}
	for _, m := range ms {
		ops[m.Op] = true
	}
	for _, op := range []string{"negate if", "drop call"} {
		if !ops[op] {
			t.Errorf("no %q mutant among %v", op, ms)
		}
	}
	a0, f0, l0, reach := run(t, -1)
	if a0 != 2 || !f0 || l0 != 2 {
		t.Fatalf("unmutated: admitted %d, full %v, logged %d", a0, f0, l0)
	}
	for _, m := range ms {
		if !reach.Reached(m.ID) {
			t.Errorf("%v not reached", m)
			continue
		}
		a, f, l, _ := run(t, m.ID)
		if a == a0 && f == f0 && l == l0 {
			t.Errorf("%v: no change", m)
		}
		t.Logf("%v: admitted %d, full %v, logged %d", m, a, f, l)
	}
}
