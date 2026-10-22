package quinthegel

import (
	"sync"
	"testing"
	"time"

	"hegel.dev/go/hegel"

	"github.com/casselc/quint-connect-go/qload"
	"github.com/casselc/quint-connect-go/qmeta"
)

// FindTrigger searches, with Hegel, for a trigger after which the system
// does not recover within `within`, and shrinks it: the extra load, the
// slowdown and the duration all shrink toward none, so the result is a
// smallest stressor that tips the system into a state it stays in. It
// returns the shrunk trigger's run, or nil if no trigger in the space
// breaks the system (Hegel's options bound the search).
func FindTrigger(t testing.TB, cfg qload.Config, base float64, space qmeta.TriggerSpace, within time.Duration, opts ...hegel.Option) *qmeta.Recovery {
	t.Helper()
	if space.MaxExtraRate == 0 {
		space.MaxExtraRate = base
	}
	return SearchTrigger(t, qmeta.OfSimulation(cfg, base, within), space, opts...)
}

// SearchTrigger is FindTrigger for any qmeta.System: Hegel draws triggers from space,
// each run with Seeds seeds, and shrinks one that most runs do not recover
// from. Unset bounds default as in FindTrigger, except MaxExtraRate (0:
// no extra load).
func SearchTrigger(t testing.TB, sys qmeta.System, space qmeta.TriggerSpace, opts ...hegel.Option) *qmeta.Recovery {
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
			tc.Errorf("no recovery after %v: goodput %.1f/s before, %.1f/s after", tr, r.Before, r.After)
		}
	}, append([]hegel.Option{hegel.SuppressHealthCheck(hegel.TooSlow), hegel.WithDatabase("")}, opts...)...)
	if err == nil {
		t.Logf("qmeta: no trigger in %d runs breaks the system", runs)
		return nil
	}
	t.Logf("qmeta: %d runs; smallest trigger found: %v (goodput %.1f/s before, %.1f/s after)", runs, found.Trigger, found.Before, found.After)
	return found
}
