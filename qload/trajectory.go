package qload

import (
	"math"
	"time"

	"github.com/casselc/quint-connect-go/qmeta"
)

// SimulateTrajectory averages runs simulations of cfg (with constant load, from the
// state from) into a trajectory for qmeta.Calibrate: the queue and orbit at each time.
func SimulateTrajectory(cfg Config, from qmeta.State, times []float64, runs int) qmeta.Trajectory {
	tr := qmeta.Trajectory{From: from, Times: times, Mean: make([]qmeta.State2, len(times))}
	horizon := 0.0
	for _, t := range times {
		horizon = math.Max(horizon, t)
	}
	cfg.Horizon = time.Duration((horizon + 1) * float64(time.Second))
	cfg.Tick = time.Second
	cfg.Initial.Queue, cfg.Initial.Orbit = from.U, from.V
	for r := 0; r < runs; r++ {
		cfg.Seed = uint64(r + 1)
		m := Simulate(cfg)
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
