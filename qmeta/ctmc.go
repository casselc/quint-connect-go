// Package qmeta predicts metastable failures with a continuous-time Markov
// chain (CTMC), following Alvaro et al., "Formal Analysis of Metastable
// Failures in Software Systems" (2025). A request-response server is
// abstracted to (u, v): u requests in the system (queued or in service)
// and v in the orbit, waiting to be retried. From those few numbers the
// chain predicts, in seconds, what simulation (qload) takes minutes and a
// stress test hours to show:
//   - Field: the most likely direction of the dynamics at every state, a
//     picture of where a system tips into a retry storm;
//   - HittingTimes: the expected time to recover from any state, which
//     grows by orders of magnitude where the system is metastable;
//   - Calibrate: fit the chain's rates to simulated trajectories, since the
//     abstraction averages away timers and retry counts.
//
// The chain is an abstraction of qload's semantics, and is checked against
// it: calibrate on short simulations, then confirm predictions with long
// ones and, last, with the real system.
package qmeta

import (
	"fmt"
	"math"
)

// Params are a single-server system's parameters (the papers' DSL).
type Params struct {
	Lambda  float64 // arrival rate of new requests (per second)
	Mu      float64 // service rate of one worker (per second)
	Workers int     // default 1
	Timeout float64 // seconds a client waits for an attempt
	Retries int     // retries after the first attempt
	Queue   int     // the most requests in the system (u's bound)
	Orbit   int     // the bound of the orbit in the finite chain (default 2*Queue)
}

// Chain is the CTMC of a single server with timeouts and retries.
type Chain struct {
	P     Params
	N, V  int // u in [0, N], v in [0, V]
	alpha float64
	fail  []float64 // fail[u]: probability a request arriving at u times out
}

// State is a (u, v) pair.
type State struct{ U, V int }

// New builds the chain of p.
func New(p Params) *Chain {
	if p.Workers == 0 {
		p.Workers = 1
	}
	if p.Orbit == 0 {
		p.Orbit = 2 * p.Queue
	}
	c := &Chain{P: p, N: p.Queue, V: p.Orbit, alpha: float64(p.Retries) / float64(p.Retries+1)}
	c.fail = make([]float64, c.N+1)
	for u := 0; u <= c.N; u++ {
		c.fail[u] = failProb(u, p.Workers, p.Mu, p.Timeout)
	}
	return c
}

// failProb is the probability that a request arriving with u requests
// ahead does not complete within the timeout: fewer than u-w+1 services
// (w workers busy at rate mu each) finish within it. A Poisson tail.
func failProb(u, workers int, mu, timeout float64) float64 {
	if u < workers {
		// served at once: fails only if its own service is too long
		return math.Exp(-mu * timeout)
	}
	need := u - workers + 1 // departures before it is taken up
	m := float64(workers) * mu * timeout
	// P(N(timeout) < need) + P(it is taken up but its own service is too
	// long) ~ P(N(timeout) < need+1): the departures the request needs,
	// its own included
	sum, term := 0.0, math.Exp(-m)
	for i := 0; i <= need; i++ {
		sum += term
		term *= m / float64(i+1)
	}
	return math.Min(1, sum)
}

// Index of a state in vectors over the chain.
func (c *Chain) Index(s State) int { return s.U*(c.V+1) + s.V }

// States is the number of states.
func (c *Chain) States() int { return (c.N + 1) * (c.V + 1) }

// Transition is a rate to a neighbouring state.
type Transition struct {
	To   State
	Rate float64
}

// Out lists the transitions out of s.
func (c *Chain) Out(s State) []Transition {
	u, v := s.U, s.V
	var out []Transition
	add := func(du, dv int, rate float64) {
		to := State{u + du, v + dv}
		if rate <= 0 || to.U < 0 || to.U > c.N || to.V < 0 || to.V > c.V {
			return
		}
		out = append(out, Transition{to, rate})
	}
	r := c.fail[u]
	tau, lam := c.P.Timeout, c.P.Lambda
	add(1, 1, lam*r)
	add(1, 0, lam*(1-r))
	if u > 0 {
		add(-1, 0, float64(min(u, c.P.Workers))*c.P.Mu)
	}
	fv := float64(v)
	add(1, 0, c.alpha*fv*r/tau)
	add(1, -1, c.alpha*fv*(1-r)/tau)
	add(0, -1, (1-c.alpha)*fv/tau)
	return out
}

// Drift is the expected rate of change of (u, v) at a state: the field the
// papers visualize.
type Drift struct {
	State
	DU, DV float64
}

// Field returns the drift at every state on a grid with the given steps.
func (c *Chain) Field(stepU, stepV int) []Drift {
	var out []Drift
	for u := 0; u <= c.N; u += stepU {
		for v := 0; v <= c.V; v += stepV {
			d := Drift{State: State{u, v}}
			for _, t := range c.Out(d.State) {
				d.DU += t.Rate * float64(t.To.U-u)
				d.DV += t.Rate * float64(t.To.V-v)
			}
			out = append(out, d)
		}
	}
	return out
}

// HittingTimes returns the expected time to reach the set target from
// every state (0 in target), solving the linear system
//
//	q(x) h(x) - sum_y Q(x,y) h(y) = 1,  x not in target
//
// exactly. States are ordered u-major and transitions change u and v by at
// most one, so the system is banded (width about V) and a banded
// elimination solves it in O(states * V^2): the hitting times of a
// metastable chain are too large for iterative methods to converge.
func (c *Chain) HittingTimes(target func(State) bool) []float64 {
	n, b := c.States(), c.V+2 // half bandwidth
	w := 2*b + 1
	a := make([]float64, n*w) // a[i*w + (j-i+b)]
	rhs := make([]float64, n)
	at := func(i, j int) *float64 { return &a[i*w+(j-i+b)] }
	for u := 0; u <= c.N; u++ {
		for v := 0; v <= c.V; v++ {
			s := State{u, v}
			i := c.Index(s)
			if target(s) {
				*at(i, i) = 1
				continue
			}
			q := 0.0
			for _, t := range c.Out(s) {
				q += t.Rate
				*at(i, c.Index(t.To)) -= t.Rate
			}
			if q == 0 {
				*at(i, i) = 1 // absorbing outside the target: never reached
				rhs[i] = math.Inf(1)
				continue
			}
			*at(i, i) += q
			rhs[i] = 1
		}
	}
	// Gaussian elimination within the band (diagonally dominant: no pivoting)
	for k := 0; k < n; k++ {
		piv := *at(k, k)
		for i := k + 1; i <= min(n-1, k+b); i++ {
			f := *at(i, k) / piv
			if f == 0 {
				continue
			}
			for j := k; j <= min(n-1, k+b); j++ {
				*at(i, j) -= f * *at(k, j)
			}
			rhs[i] -= f * rhs[k]
		}
	}
	h := make([]float64, n)
	for i := n - 1; i >= 0; i-- {
		s := rhs[i]
		for j := i + 1; j <= min(n-1, i+b); j++ {
			s -= *at(i, j) * h[j]
		}
		h[i] = s / *at(i, i)
	}
	return h
}

// Expect returns the expected state (E[u], E[v]) at each of times, from
// the state from, by uniformization: X(t) ~ sum_k Poisson(Lt; k) P^k with
// P = I + Q/L.
func (c *Chain) Expect(from State, times []float64) []State2 {
	n := c.States()
	L := 0.0
	for u := 0; u <= c.N; u++ {
		for v := 0; v <= c.V; v++ {
			q := 0.0
			for _, t := range c.Out(State{u, v}) {
				q += t.Rate
			}
			L = math.Max(L, q)
		}
	}
	L *= 1.01
	type edge struct {
		to   int
		prob float64
	}
	edges := make([][]edge, n)
	stay := make([]float64, n)
	for u := 0; u <= c.N; u++ {
		for v := 0; v <= c.V; v++ {
			i := c.Index(State{u, v})
			stay[i] = 1
			for _, t := range c.Out(State{u, v}) {
				edges[i] = append(edges[i], edge{c.Index(t.To), t.Rate / L})
				stay[i] -= t.Rate / L
			}
		}
	}
	pi := make([]float64, n)
	pi[c.Index(from)] = 1
	next := make([]float64, n)
	acc := make([][]float64, len(times))
	for i := range acc {
		acc[i] = make([]float64, n)
	}
	maxT := 0.0
	for _, t := range times {
		maxT = math.Max(maxT, t)
	}
	K := int(L*maxT + 10*math.Sqrt(L*maxT) + 50)
	// Poisson weights per time, in log space for large Lt
	for k := 0; k <= K; k++ {
		for ti, t := range times {
			lt := L * t
			var wk float64
			if lt == 0 {
				if k == 0 {
					wk = 1
				}
			} else {
				lg, _ := math.Lgamma(float64(k + 1))
				wk = math.Exp(float64(k)*math.Log(lt) - lt - lg)
			}
			if wk < 1e-300 {
				continue
			}
			for i, p := range pi {
				acc[ti][i] += wk * p
			}
		}
		for i := range next {
			next[i] = 0
		}
		for i, p := range pi {
			if p == 0 {
				continue
			}
			next[i] += p * stay[i]
			for _, e := range edges[i] {
				next[e.to] += p * e.prob
			}
		}
		pi, next = next, pi
	}
	out := make([]State2, len(times))
	for ti := range times {
		for u := 0; u <= c.N; u++ {
			for v := 0; v <= c.V; v++ {
				p := acc[ti][c.Index(State{u, v})]
				out[ti].U += p * float64(u)
				out[ti].V += p * float64(v)
			}
		}
	}
	return out
}

// State2 is an expected (u, v).
type State2 struct{ U, V float64 }

func (s State) String() string { return fmt.Sprintf("(%d,%d)", s.U, s.V) }
