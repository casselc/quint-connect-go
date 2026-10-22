package quinthegel

import (
	"sync"
	"testing"
	"time"

	"hegel.dev/go/hegel"

	"github.com/casselc/quint-connect-go/qmeta"
)

// A TriggerSearch is what FindTrigger found: the shrunk trigger's run, or
// nil if no trigger in the space breaks the system.
type TriggerSearch struct {
	Found *qmeta.Recovery
	Runs  int // test cases run
}

// Expect states whether the search should find a trigger: one that
// breaks the system, or none in the space. It fails t with the shrunk
// trigger's run when one was not expected, and returns the run.
func (s *TriggerSearch) Expect(t testing.TB, found bool) *qmeta.Recovery {
	t.Helper()
	switch {
	case found && s.Found == nil:
		t.Fatalf("quinthegel: no trigger in %d runs breaks the system; want one (widen the space or raise the test cases)", s.Runs)
	case !found && s.Found != nil:
		t.Fatalf("quinthegel: found a trigger the system does not recover from; want none: %v", *s.Found)
	}
	return s.Found
}

// FindTrigger searches, with Hegel, for a trigger after which the system
// does not recover, and shrinks it: the extra load, the slowdown and the
// duration all shrink toward none, so the result is a smallest stressor
// that tips the system into a state it stays in. The system is a qload
// simulation (qmeta.OfSimulation), an application's experiment (qmeta.Of)
// or anything else; each trigger runs with space.Seeds seeds, and breaks
// the system when most do not recover (qmeta.Mostly). Unset bounds
// default: no extra load, no slowdown, 300 s; Hegel's options bound the
// search. State what it should find with Expect.
func FindTrigger(t testing.TB, sys qmeta.System, space qmeta.TriggerSpace, opts ...hegel.Option) *TriggerSearch {
	t.Helper()
	if space.MaxSlowdown == 0 {
		space.MaxSlowdown = 1
	}
	if space.MaxDuration == 0 {
		space.MaxDuration = 300 * time.Second
	}
	if space.Seeds == 0 {
		space.Seeds = 3
	}
	var mu sync.Mutex
	var found *qmeta.Recovery
	runs := 0
	err := hegel.Run(func(tc hegel.TestCase) {
		tr := qmeta.Trigger{
			Start:     space.Start,
			ExtraRate: float64(hegel.Draw(tc, hegel.Integers(0, int(space.MaxExtraRate*10)))) / 10,
			Slowdown:  1 + float64(hegel.Draw(tc, hegel.Integers(0, int((space.MaxSlowdown-1)*10))))/10,
			Duration:  time.Duration(hegel.Draw(tc, hegel.Integers(0, int(space.MaxDuration/time.Second)))) * time.Second,
		}
		ok, r := qmeta.Mostly(sys, tr, space.Seeds)
		mu.Lock()
		runs++
		if !ok {
			found = &r
		}
		mu.Unlock()
		if !ok {
			tc.Errorf("no recovery after %v", r)
		}
	}, append([]hegel.Option{hegel.SuppressHealthCheck(hegel.TooSlow), hegel.WithDatabase("")}, opts...)...)
	if err == nil {
		t.Logf("qmeta: no trigger in %d runs breaks the system", runs)
		return &TriggerSearch{Runs: runs}
	}
	t.Logf("qmeta: %d runs; smallest trigger found: %v", runs, *found)
	return &TriggerSearch{Found: found, Runs: runs}
}
