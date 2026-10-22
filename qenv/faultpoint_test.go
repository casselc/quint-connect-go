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
	qenv.DeclareFault("test.write", "qenv_test.write")
	qenv.DeclareFault("test.never", "qenv_test.never")
	var runs [][]qenv.Choice
	var failed, crashed int
	for seed := uint64(1); seed <= 20; seed++ {
		seeded := qenv.NewSeeded(seed)
		seeded.Rates = qenv.Rates{Default: 0.5}
		rec := &qenv.Recorder{C: seeded}
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
