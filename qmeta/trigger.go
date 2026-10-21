package qmeta

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"hegel.dev/go/hegel"

	"github.com/casselc/quint-connect-go/qload"
)

// A Trigger is a transient stressor: for Duration, starting at Start, the
// load is ExtraRate requests per second above the base rate and service
// times are multiplied by Slowdown (1: none).
type Trigger struct {
	Start     time.Duration
	Duration  time.Duration
	ExtraRate float64
	Slowdown  float64
}

func (tr Trigger) String() string {
	return fmt.Sprintf("+%.1f req/s and %.2gx slower for %v", tr.ExtraRate, tr.Slowdown, tr.Duration)
}

// Recovery is the outcome of one run with a trigger.
type Recovery struct {
	Trigger   Trigger
	Recovered bool
	// Goodput (successes per second) over the last 100 s before the
	// trigger, and over the check window after it.
	Before, After float64
	Metrics       *qload.Metrics
}

// Recovers runs cfg (whose client rate is replaced by base, plus the
// trigger) and checks the recovery property: within `within` after the
// stressor ends, goodput is back to at least 90% of what it was before it.
// A metastable system fails it: the stressor is gone, the degradation is
// not.
func Recovers(cfg qload.Config, base float64, tr Trigger, within time.Duration) Recovery {
	if tr.Start == 0 {
		tr.Start = 100 * time.Second
	}
	if tr.Slowdown == 0 {
		tr.Slowdown = 1
	}
	end := tr.Start + tr.Duration
	cfg.Client.Rate = qload.Spike(base, base+tr.ExtraRate, tr.Start, end)
	cfg.Server.Slowdown = qload.Spike(1, tr.Slowdown, tr.Start, end)
	cfg.Horizon = end + within + 100*time.Second
	m := qload.Simulate(cfg)
	goodput := func(s qload.Sample) float64 { return float64(s.Successes) }
	r := Recovery{Trigger: tr, Metrics: m,
		Before: m.Mean(tr.Start-100*time.Second, tr.Start, goodput),
		After:  m.Mean(end+within, cfg.Horizon, goodput)}
	r.Recovered = r.After >= 0.9*r.Before
	return r
}

// TriggerSpace bounds the triggers FindTrigger draws.
type TriggerSpace struct {
	MaxExtraRate float64       // default: the base rate
	MaxSlowdown  float64       // default 1 (no capacity triggers)
	MaxDuration  time.Duration // default 300 s
	// Seeds per trigger (default 3): a trigger breaks the system when most
	// of its runs do not recover, so the noise of one run is not mistaken
	// for the trigger's effect.
	Seeds int
}

// RecoversMostly runs the trigger with seeds 1..n and reports whether most
// runs recover, with the first run that did not (if any).
func RecoversMostly(cfg qload.Config, base float64, tr Trigger, within time.Duration, n int) (bool, Recovery) {
	stuck, first := 0, Recovery{}
	for i := 1; i <= n; i++ {
		cfg.Seed = uint64(i)
		r := Recovers(cfg, base, tr, within)
		if !r.Recovered {
			if stuck == 0 {
				first = r
			}
			stuck++
		}
	}
	return 2*stuck <= n, first
}

// FindTrigger searches, with Hegel, for a trigger after which the system
// does not recover within `within`, and shrinks it: the extra load, the
// slowdown and the duration all shrink toward none, so the result is a
// smallest stressor that tips the system into a state it stays in. It
// returns the shrunk trigger's run, or nil if no trigger in the space
// breaks the system (Hegel's options bound the search).
func FindTrigger(t testing.TB, cfg qload.Config, base float64, space TriggerSpace, within time.Duration, opts ...hegel.Option) *Recovery {
	t.Helper()
	if space.MaxExtraRate == 0 {
		space.MaxExtraRate = base
	}
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
	var found *Recovery
	runs := 0
	err := hegel.Run(func(tc hegel.TestCase) {
		tr := Trigger{
			ExtraRate: float64(hegel.Draw(tc, hegel.Integers(0, int(space.MaxExtraRate*10)))) / 10,
			Slowdown:  1 + float64(hegel.Draw(tc, hegel.Integers(0, int((space.MaxSlowdown-1)*10))))/10,
			Duration:  time.Duration(hegel.Draw(tc, hegel.Integers(0, int(space.MaxDuration/time.Second)))) * time.Second,
		}
		ok, r := RecoversMostly(cfg, base, tr, within, space.Seeds)
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
