package qmeta

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// An Observation is a running system's state sampled at time T (seconds):
// the requests at its server (u) and the operations waiting to retry (v),
// as qrole measures them.
type Observation struct {
	T    float64
	U, V int
}

// Trajectories turns a sampled time series into trajectories for
// Calibrate: from each sample, the states observed the given times later
// (the nearest sample), averaged over the samples that start in the same
// cell of bucket by bucket states. Samples should be regular, and each
// cell's trajectory is only as good as the samples that start in it.
func Trajectories(obs []Observation, times []float64, bucket int) []Trajectory {
	if bucket < 1 {
		bucket = 1
	}
	sort.Slice(obs, func(i, j int) bool { return obs[i].T < obs[j].T })
	type acc struct {
		from State
		sum  []State2
		n    int
	}
	cells := map[State]*acc{}
	var order []State
	at := func(t float64) (Observation, bool) {
		i := sort.Search(len(obs), func(i int) bool { return obs[i].T >= t })
		if i == len(obs) {
			return Observation{}, false
		}
		if i > 0 && t-obs[i-1].T < obs[i].T-t {
			i--
		}
		return obs[i], true
	}
	for _, o := range obs {
		var later []State2
		for _, dt := range times {
			p, ok := at(o.T + dt)
			if !ok || p.T < o.T+dt/2 {
				later = nil
				break
			}
			later = append(later, State2{float64(p.U), float64(p.V)})
		}
		if later == nil {
			continue
		}
		cell := State{o.U / bucket * bucket, o.V / bucket * bucket}
		a, ok := cells[cell]
		if !ok {
			a = &acc{from: cell, sum: make([]State2, len(times))}
			cells[cell] = a
			order = append(order, cell)
		}
		for i, s := range later {
			a.sum[i].U += s.U
			a.sum[i].V += s.V
		}
		a.n++
	}
	var out []Trajectory
	for _, cell := range order {
		a := cells[cell]
		tr := Trajectory{From: a.from, Times: times, Mean: make([]State2, len(times))}
		for i := range a.sum {
			tr.Mean[i] = State2{a.sum[i].U / float64(a.n), a.sum[i].V / float64(a.n)}
		}
		out = append(out, tr)
	}
	return out
}

// A Region is the set of a chain's states from which the system is more
// likely than not to be still unhealthy after Bound seconds: where it does
// not come back by itself in time. Its boundary is an alert threshold with
// a derivation (Rules).
//
// The region is defined by the probability of recovery (RecoveryWithin),
// not the expected time (HittingTimes): near a storm the expected time is
// dominated by the rare runs that tip into it, and a region of expected
// times would alert on states that drain in seconds nearly always.
type Region struct {
	Chain *Chain
	Bound float64
	// Healthy is the healthy set, if the region was made from one
	// (HealthyRegion): Rules then states it, and its PromQL, itself.
	Healthy *Healthy
	// MinV[u] is the smallest v with u at which the state is in the
	// region (-1: none at this u).
	MinV []int
	p    []float64
	hit  []float64
}

// Healthy is a healthy set of states: a queue of at most MaxU requests,
// whatever the orbit. One value gives the region its predicate
// (Contains), the rules their description (String) and the condition a
// dashboard or a recovery check reads off the same gauge (PromQL), so the
// three cannot drift apart.
type Healthy struct{ MaxU int }

// Contains reports whether s is healthy.
func (h Healthy) Contains(s State) bool { return s.U <= h.MaxU }

func (h Healthy) String() string { return fmt.Sprintf("u <= %d", h.MaxU) }

// PromQL is the healthy condition over cfg's queue metric (qrole's
// quint_serve_inflight by default), for cfg's server.
func (h Healthy) PromQL(cfg RuleConfig) string {
	q := cfg.Queue
	if q == "" {
		q = "quint_serve_inflight"
	}
	return fmt.Sprintf("%s{name=%q} <= %d", q, cfg.Server, h.MaxU)
}

// HealthyRegion is Region of a Healthy set, which the region keeps for
// Rules.
func (c *Chain) HealthyRegion(h Healthy, bound float64) *Region {
	r := c.Region(h.Contains, bound)
	r.Healthy = &h
	return r
}

// Region returns the states from which the probability of having been
// healthy within bound seconds is below one half.
func (c *Chain) Region(healthy func(State) bool, bound float64) *Region {
	r := &Region{Chain: c, Bound: bound, p: c.RecoveryWithin(healthy, bound), hit: c.HittingTimes(healthy), MinV: make([]int, c.N+1)}
	for u := 0; u <= c.N; u++ {
		r.MinV[u] = -1
		for v := 0; v <= c.V; v++ {
			if r.p[c.Index(State{u, v})] < 0.5 {
				r.MinV[u] = v
				break
			}
		}
	}
	return r
}

// Contains reports whether s (clamped to the chain's bounds) is in the
// region.
func (r *Region) Contains(s State) bool {
	return r.p[r.Chain.Index(r.Chain.Clamp(s))] < 0.5
}

// Recovered is the probability of having been healthy within the bound
// from s.
func (r *Region) Recovered(s State) float64 {
	return r.p[r.Chain.Index(r.Chain.Clamp(s))]
}

// Recovery is the expected time to become healthy from s.
func (r *Region) Recovery(s State) float64 {
	return r.hit[r.Chain.Index(r.Chain.Clamp(s))]
}

// A Band is a range of u, [ULo, UHi), over which the region starts at the
// same v: an alert fires there when v >= VMin.
type Band struct {
	ULo, UHi, VMin int
}

// Bands is the region's boundary, as bands of u at most step wide (step
// <= 1: exact). A band's VMin is the largest of the boundary's over its u,
// so a coarse band never covers a state outside the region. Above a band's
// VMin the region is taken to continue (it does, for a chain whose
// recovery slows with the orbit); the exact bands' exceptions are counted
// by Holes.
func (r *Region) Bands(step int) []Band {
	step = max(step, 1)
	var out []Band
	for u := 0; u <= r.Chain.N; u++ {
		v := r.MinV[u]
		if v < 0 {
			continue
		}
		if n := len(out); n > 0 && out[n-1].UHi == u && (out[n-1].VMin == v || u-out[n-1].ULo < step) {
			out[n-1].UHi = u + 1
			out[n-1].VMin = max(out[n-1].VMin, v)
			continue
		}
		out = append(out, Band{u, u + 1, v})
	}
	// adjacent bands with equal VMin (after coarsening) are one
	var merged []Band
	for _, b := range out {
		if n := len(merged); n > 0 && merged[n-1].UHi == b.ULo && merged[n-1].VMin == b.VMin {
			merged[n-1].UHi = b.UHi
			continue
		}
		merged = append(merged, b)
	}
	return merged
}

// Holes counts the states above a band's VMin that are not in the region.
func (r *Region) Holes() int {
	n := 0
	for u := 0; u <= r.Chain.N; u++ {
		if r.MinV[u] < 0 {
			continue
		}
		for v := r.MinV[u]; v <= r.Chain.V; v++ {
			if !r.Contains(State{u, v}) {
				n++
			}
		}
	}
	return n
}

// RuleConfig names what an alerting rule watches.
type RuleConfig struct {
	Alert   string // the alert's name (default "Metastable")
	Server  string // qrole's server name (the queue's "name" label)
	Service string // qrole's service (the orbit's "service" label)
	For     string // how long the state must hold (default "1m")
	// Queue and Orbit are the metrics (default qrole's, as Prometheus
	// names them: quint_serve_inflight, quint_orbit).
	Queue, Orbit string
	// Step is the widest band of u in the rule (default 10): the region's
	// boundary, coarsened to fire only inside it (Bands).
	Step int
	// Headroom, if set, is noted in the rule: the smallest trigger found
	// to tip the system (quinthegel.FindTrigger).
	Headroom string
}

// Rules is a Prometheus alerting rule group that fires while the system's
// state is in the region: its boundary, band by band, over qrole's metrics.
// The derivation (the chain's parameters, the healthy set, the bound) goes
// in the annotations, so a change to the retry policy in
// code shows up as a changed rule. healthy describes the healthy set; ""
// takes the region's own (HealthyRegion), whose PromQL is annotated too.
func (r *Region) Rules(cfg RuleConfig, healthy string) string {
	if healthy == "" && r.Healthy != nil {
		healthy = r.Healthy.String()
	}
	if cfg.Alert == "" {
		cfg.Alert = "Metastable"
	}
	if cfg.For == "" {
		cfg.For = "1m"
	}
	if cfg.Step == 0 {
		cfg.Step = 10
	}
	if cfg.Queue == "" {
		cfg.Queue = "quint_serve_inflight"
	}
	if cfg.Orbit == "" {
		cfg.Orbit = "quint_orbit"
	}
	q := fmt.Sprintf("%s{name=%q}", cfg.Queue, cfg.Server)
	o := fmt.Sprintf("%s{service=%q}", cfg.Orbit, cfg.Service)
	var terms []string
	for _, b := range r.Bands(cfg.Step) {
		cond := fmt.Sprintf("%s >= %d", q, b.ULo)
		if b.UHi <= r.Chain.N {
			cond += fmt.Sprintf(" and %s < %d", q, b.UHi)
		}
		terms = append(terms, fmt.Sprintf("(%s and on() %s >= %d)", cond, o, b.VMin))
	}
	expr := "vector(0) > 1" // no state is in the region: never fires
	if len(terms) > 0 {
		expr = strings.Join(terms, "\n        or ")
	}
	p := r.Chain.P
	var b strings.Builder
	fmt.Fprintf(&b, "groups:\n  - name: quint-metastability\n    rules:\n      - alert: %s\n", cfg.Alert)
	fmt.Fprintf(&b, "        expr: |\n          %s\n", strings.ReplaceAll(expr, "\n        ", "\n          "))
	fmt.Fprintf(&b, "        for: %s\n", cfg.For)
	fmt.Fprintf(&b, "        labels:\n          server: %q\n", cfg.Server)
	fmt.Fprintf(&b, "        annotations:\n")
	fmt.Fprintf(&b, "          summary: %q\n", fmt.Sprintf("%s is in a state it is unlikely to leave by itself within %s", cfg.Server, seconds(r.Bound)))
	fmt.Fprintf(&b, "          derivation: %q\n", fmt.Sprintf("qmeta chain: lambda=%.3g/s mu=%.3g/s workers=%d timeout=%.3gs retries=%d backoff=%.3gs budget=%.3g; region: states with less than even odds of %s within %s",
		p.Lambda, p.Mu, max(p.Workers, 1), p.Timeout, p.Retries, p.Backoff, p.RetryBudget, healthy, seconds(r.Bound)))
	if r.Healthy != nil {
		fmt.Fprintf(&b, "          healthy: %q\n", r.Healthy.PromQL(cfg))
	}
	if cfg.Headroom != "" {
		fmt.Fprintf(&b, "          headroom: %q\n", cfg.Headroom)
	}
	return b.String()
}

func seconds(s float64) string {
	if math.IsInf(s, 1) {
		return "ever"
	}
	return fmt.Sprintf("%.3gs", s)
}
