package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/casselc/quint-connect-go/qenv"
)

// Woven, the store's points inject its own error and crashes in a
// simulation, and coverage names the point no run reached. Unwoven, no
// fault happens and nothing is declared.
func TestFaultPointsWoven(t *testing.T) {
	var runs [][]qenv.Choice
	var unavailable, restarts int
	for seed := uint64(1); seed <= 30; seed++ {
		c := qenv.NewSeeded(seed)
		c.Rates = qenv.Rates{Default: 0.3}
		rec := &qenv.Recorder{C: c}
		sim := qenv.New(rec)
		st := New()
		p := sim.Process("app", false, func(ctx context.Context) {
			for i := range 5 {
				if err := st.Put(ctx, "k", "v"); err != nil {
					t.Error(err)
				}
				if _, err := st.Get(ctx, "k"); errors.Is(err, ErrUnavailable) {
					unavailable++
				} else if err != nil {
					t.Error(err)
				}
				qenv.Sleep(ctx, time.Duration(i+1)*time.Second)
			}
		})
		if err := sim.Run(time.Minute); err != nil {
			t.Fatal(err)
		}
		restarts += p.Incarnation - 1
		sim.Close()
		runs = append(runs, rec.Choices)
	}
	cov := qenv.CoverageOf(runs)
	t.Log(cov)
	if unavailable == 0 || restarts == 0 {
		t.Fatalf("%d ErrUnavailable, %d restarts", unavailable, restarts)
	}
	if u := cov.Unreached(); len(u) != 1 || u[0].Kind != "store.flush" || u[0].Sites[0] != "Flush" {
		t.Errorf("unreached: %v", u)
	}
	if err := (&Store{}).Flush(context.Background()); err != nil {
		t.Errorf("outside a simulation: %v", err)
	}
}
