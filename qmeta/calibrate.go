package qmeta

import (
	"fmt"
)

// Trajectory is the mean simulated (u, v) at times (seconds) from a state.
type Trajectory struct {
	From  State
	Times []float64
	Mean  []State2
}

// Loss is the squared distance between the chain's expected trajectories
// and simulated ones. Trajectories with the same times share one backward
// pass (MeanAll), so a series' many cells cost what one does.
func (c *Chain) Loss(data []Trajectory) float64 {
	loss := 0.0
	all := map[string][][]State2{}
	for _, tr := range data {
		key := fmt.Sprint(tr.Times)
		if _, ok := all[key]; !ok {
			all[key] = c.MeanAll(tr.Times)
		}
		i0 := c.Index(c.Clamp(tr.From))
		exp := make([]State2, len(tr.Times))
		for i := range exp {
			exp[i] = all[key][i][i0]
		}
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
	return fit(p, data, []int{0, 1})
}

// CalibrateTimeout is Calibrate with the arrival rate held: fit to a
// running system, whose rate of new operations is measured (qrole's
// operation counter), not inferred. A series taken mostly in a storm cannot
// tell a higher arrival rate from a shorter timeout, and a fitted rate at
// or above the service rate makes every state look unrecoverable.
func CalibrateTimeout(p Params, data []Trajectory) (Params, float64) {
	return fit(p, data, []int{1})
}

// Warning says when p cannot describe a system that recovers at all: an
// arrival rate at or above the service capacity makes every state look
// unrecoverable. Set by hand that is an overload; from Calibrate it means
// the data were mostly taken in a storm, which cannot tell more arrivals
// from a shorter timeout.
func (p Params) Warning() string {
	w := p.Workers
	if w == 0 {
		w = 1
	}
	if p.Mu <= 0 || p.Lambda < p.Mu*float64(w) {
		return ""
	}
	return fmt.Sprintf("λ=%.3g ≥ capacity %.3g (μ=%.3g × %d workers): every state looks unrecoverable; if calibrated, the data are mostly from a storm: hold λ (CalibrateTimeout)", p.Lambda, p.Mu*float64(w), p.Mu, w)
}

func fit(p Params, data []Trajectory, dims []int) (Params, float64) {
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
			for _, dim := range dims {
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
