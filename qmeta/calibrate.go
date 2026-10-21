package qmeta

import (
	"math"
	"time"

	"github.com/casselc/quint-connect-go/qload"
)

// Trajectory is the mean simulated (u, v) at times (seconds) from a state.
type Trajectory struct {
	From  State
	Times []float64
	Mean  []State2
}

// Simulate averages runs simulations of cfg (with constant load, from the
// state from) into a trajectory: qload's queue and orbit at each time.
func Simulate(cfg qload.Config, from State, times []float64, runs int) Trajectory {
	tr := Trajectory{From: from, Times: times, Mean: make([]State2, len(times))}
	horizon := 0.0
	for _, t := range times {
		horizon = math.Max(horizon, t)
	}
	cfg.Horizon = time.Duration((horizon + 1) * float64(time.Second))
	cfg.Tick = time.Second
	cfg.Initial.Queue, cfg.Initial.Orbit = from.U, from.V
	for r := 0; r < runs; r++ {
		cfg.Seed = uint64(r + 1)
		m := qload.Simulate(cfg)
		for i, t := range times {
			k := int(t) - 1 // sample k is taken at (k+1) s
			if k < 0 || k >= len(m.Samples) {
				continue
			}
			tr.Mean[i].U += float64(m.Samples[k].Queue) / float64(runs)
			tr.Mean[i].V += float64(m.Samples[k].Orbit) / float64(runs)
		}
	}
	return tr
}

// Loss is the squared distance between the chain's expected trajectories
// and simulated ones.
func (c *Chain) Loss(data []Trajectory) float64 {
	loss := 0.0
	for _, tr := range data {
		exp := c.Expect(tr.From, tr.Times)
		for i := range exp {
			du, dv := exp[i].U-tr.Mean[i].U, exp[i].V-tr.Mean[i].V
			loss += du*du + dv*dv
		}
	}
	return loss
}

// Calibrate fits the arrival rate and timeout of p so that the chain's
// expected trajectories match simulated ones: least squares, by
// coordinate descent from the nominal values, with a small pull toward
// them (the papers' loss). The chain averages away what the simulation
// keeps (individual timers, retry counts), so the fitted values differ
// from the nominal ones; predictions should use the fitted chain.
func Calibrate(p Params, data []Trajectory) (Params, float64) {
	nominal := p
	loss := func(q Params) float64 {
		reg := sq((q.Lambda-nominal.Lambda)/nominal.Lambda) + sq((q.Timeout-nominal.Timeout)/nominal.Timeout)
		return New(q).Loss(data) * (1 + reg)
	}
	best := loss(p)
	steps := []float64{0.1, 0.05, 0.02, 0.01}
	for _, step := range steps {
		for improved := true; improved; {
			improved = false
			for _, dim := range []int{0, 1} {
				for _, dir := range []float64{1 + step, 1 - step} {
					q := p
					if dim == 0 {
						q.Lambda *= dir
					} else {
						q.Timeout *= dir
					}
					if l := loss(q); l < best {
						p, best, improved = q, l, true
					}
				}
			}
		}
	}
	return p, best
}

func sq(x float64) float64 { return x * x }
