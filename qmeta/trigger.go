package qmeta

import (
	"fmt"
	"math"
	"runtime"
	"sync"
	"time"

	"github.com/casselc/quint-connect-go/qload"
)

// A Trigger is a transient stressor (qload.Trigger).
type Trigger = qload.Trigger

// Window is the length of the windows goodput is compared over: before the
// trigger, and after the check window.
const Window = 100 * time.Second

// Recovery is the outcome of one run with a trigger.
type Recovery struct {
	Trigger   Trigger
	Recovered bool
	// Goodput (per second) over the Window before the trigger, and over the
	// Window after the check window.
	Before, After float64
	// Baseline, if set, is the healthy goodput Judge compares After with,
	// instead of Before (Experiment.Healthy).
	Baseline float64
	// Offered is the open-loop load (per second) before the trigger, if
	// known: what a healthy system's goodput is near, for Warning.
	Offered float64
	Metrics *qload.Metrics // a qload.Simulate run's
	Series  *qload.Series  // an Experiment's
}

func (r Recovery) String() string {
	v := "recovered"
	if !r.Recovered {
		v = "did not recover"
	}
	s := fmt.Sprintf("%v: %s (goodput %.1f/s before, %.1f/s after", r.Trigger, v, r.Before, r.After)
	if r.Baseline > 0 {
		s += fmt.Sprintf(", healthy %.1f/s", r.Baseline)
	}
	s += ")"
	if w := r.Warning(); w != "" {
		s += "; " + w
	}
	return s
}

// Warning says why the verdict may mean little: judged against the
// goodput before the trigger, by a system that was not healthy then. A
// system already collapsed "recovers" to its collapsed goodput.
func (r Recovery) Warning() string {
	if r.Baseline > 0 || r.Offered <= 0 || r.Before >= 0.5*r.Offered {
		return ""
	}
	return fmt.Sprintf("goodput before the trigger was %.1f/s against %.1f offered: the system was not healthy before it; set Experiment.Healthy (or lower the load)", r.Before, r.Offered)
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
	end := tr.End()
	cfg.Client.Rate = tr.Rate(base)
	cfg.Server.Slowdown = tr.Factor()
	cfg.Horizon = end + within + Window
	m := qload.Simulate(cfg)
	goodput := func(s qload.Sample) float64 { return float64(s.Successes) }
	r := Recovery{Trigger: tr, Metrics: m, Offered: base,
		Before: m.Mean(tr.Start-Window, tr.Start, goodput),
		After:  m.Mean(end+within, cfg.Horizon, goodput)}
	r.Judge()
	return r
}

// Judge sets Recovered from Before and After: goodput back to at least 90%
// of what it was, give or take two standard errors of the difference
// (counts over a Window are roughly Poisson). Without the tolerance a
// healthy run at a few requests per second fails one time in ten.
func (r *Recovery) Judge() {
	base := r.Before
	if r.Baseline > 0 {
		base = r.Baseline
	}
	se := math.Sqrt((base + r.After) / Window.Seconds())
	r.Recovered = r.After >= 0.9*base-2*se
}

// Assess runs an experiment with seed through tr and judges its recovery:
// the rate of the goodput action (a model step: "markPaid") over the
// Window after `within` past the trigger's end, against the experiment's
// Healthy goodput or, if unknown, the Window before the trigger. tr.Start defaults to Window; start it later for a closed loop,
// which takes a think time to reach its steady state.
func Assess(x qload.Experiment, goodput string, tr Trigger, within time.Duration, seed uint64) Recovery {
	if tr.Start == 0 {
		tr.Start = Window
	}
	after := tr.End() + within
	series := x.Run(seed, tr, after+Window)
	r := Recovery{Trigger: tr, Series: series, Baseline: x.Healthy, Offered: x.Rate,
		Before: series.Rate(goodput, tr.Start-Window, tr.Start),
		After:  series.Rate(goodput, after, after+Window)}
	r.Judge()
	return r
}

// Of is an experiment as a System, for Mostly and Search.
func Of(x qload.Experiment, goodput string, within time.Duration) System {
	return func(tr Trigger, seed uint64) Recovery { return Assess(x, goodput, tr, within, seed) }
}

// TriggerSpace bounds the triggers quinthegel.FindTrigger draws.
type TriggerSpace struct {
	MaxExtraRate float64       // default 0 (no extra load)
	MaxSlowdown  float64       // default 1 (no capacity triggers)
	MaxDuration  time.Duration // default 300 s
	// Start of the trigger (default 100 s): late enough that the system is
	// in its steady state, which a closed loop takes a think time to reach.
	Start time.Duration
	// Seeds per trigger (default 3): a trigger breaks the system when most
	// of its runs do not recover, so the noise of one run is not mistaken
	// for the trigger's effect.
	Seeds int
}

// A System runs a trigger against a system with a seed and judges its
// recovery: a qload simulation (OfSimulation), an application's
// experiment (Of), or anything else.
type System func(tr Trigger, seed uint64) Recovery

// OfSimulation is a qload simulation as a System: Recovers with the seed.
func OfSimulation(cfg qload.Config, base float64, within time.Duration) System {
	return func(tr Trigger, seed uint64) Recovery {
		cfg.Seed = seed
		return Recovers(cfg, base, tr, within)
	}
}

// Runs runs the trigger with seeds 1..n, in parallel, and returns the
// runs in seed order. sys must be safe to call concurrently: a qenv
// simulation is, if it records through its own context (qenv.WithContext).
func Runs(sys System, tr Trigger, n int) []Recovery {
	rs := make([]Recovery, n)
	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.GOMAXPROCS(0))
	for i := range n {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			rs[i] = sys(tr, uint64(i+1))
		}()
	}
	wg.Wait()
	return rs
}

// Count is how many of seeds 1..n recover from the trigger.
func Count(sys System, tr Trigger, n int) int {
	ok := 0
	for _, r := range Runs(sys, tr, n) {
		if r.Recovered {
			ok++
		}
	}
	return ok
}

// Mostly reports whether most of seeds 1..n recover from the trigger,
// with the lowest-seeded run that did not (if any).
func Mostly(sys System, tr Trigger, n int) (bool, Recovery) {
	stuck, first := 0, Recovery{}
	for _, r := range Runs(sys, tr, n) {
		if !r.Recovered {
			if stuck == 0 {
				first = r
			}
			stuck++
		}
	}
	return 2*stuck <= n, first
}
