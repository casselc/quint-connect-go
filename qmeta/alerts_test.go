package qmeta_test

import (
	"strings"
	"testing"

	"github.com/casselc/quint-connect-go/qmeta"
)

// Each sample's later states are averaged per cell; samples whose later
// times run past the series are dropped.
func TestTrajectories(t *testing.T) {
	var obs []qmeta.Observation
	for i := range 6 {
		obs = append(obs, qmeta.Observation{T: float64(i), U: 10 * i, V: i})
	}
	trs := qmeta.Trajectories(obs, []float64{1, 2}, 20)
	// Starts at 0..3 have both later samples: cells (0,0) from t=0,1 and
	// (20,0) from t=2,3.
	if len(trs) != 2 {
		t.Fatalf("%d trajectories, want 2: %+v", len(trs), trs)
	}
	got := trs[0]
	if got.From != (qmeta.State{U: 0, V: 0}) || got.Mean[0] != (qmeta.State2{U: 15, V: 1.5}) || got.Mean[1] != (qmeta.State2{U: 25, V: 2.5}) {
		t.Errorf("first trajectory %+v", got)
	}
	if trs[1].From != (qmeta.State{U: 20, V: 0}) {
		t.Errorf("second trajectory from %+v", trs[1].From)
	}
}

// At 9.5/s the paper's chain has a region: high orbits take minutes to
// drain, the drained states do not, and the bands bound it from below. The
// rules carry the bands and the derivation. At 8/s the system is stable
// but drains a full queue slowly: its region is a fraction of 9.5/s's, and
// at an hour's bound it has none and the rule never fires.
func TestRegionAndRules(t *testing.T) {
	healthy := func(s qmeta.State) bool { return s.U <= 10 }
	r := example(9.5).Region(healthy, 120)
	if r.Contains(qmeta.State{U: 5, V: 0}) {
		t.Error("a healthy state is in the region")
	}
	if !r.Contains(qmeta.State{U: 100, V: 60}) || !r.Contains(qmeta.State{U: 500, V: 500}) {
		t.Error("the full, storming state (or past the bounds) is not in the region")
	}
	if len(r.Bands(1)) == 0 || r.Holes() > r.Chain.States()/100 {
		t.Fatalf("%d bands, %d holes", len(r.Bands(1)), r.Holes())
	}
	for _, b := range r.Bands(1) {
		if !r.Contains(qmeta.State{U: b.ULo, V: b.VMin}) || b.VMin > 0 && r.Contains(qmeta.State{U: b.ULo, V: b.VMin - 1}) {
			t.Errorf("band %+v is not the region's boundary", b)
		}
	}
	coarse := r.Bands(10)
	if len(coarse) >= len(r.Bands(1)) {
		t.Errorf("coarse bands %v", coarse)
	}
	for _, b := range coarse {
		for u := b.ULo; u < b.UHi; u++ {
			if !r.Contains(qmeta.State{U: u, V: b.VMin}) {
				t.Errorf("coarse band %+v covers (%d,%d), outside the region", b, u, b.VMin)
			}
		}
	}
	rules := r.Rules(qmeta.RuleConfig{Server: "api", Service: "api", Headroom: "spike of 40/s for 10s"}, "u <= 10")
	for _, want := range []string{
		"- alert: Metastable",
		`quint_serve_inflight{name="api"} >= `,
		`and on() quint_orbit{service="api"} >= `,
		"for: 1m",
		"lambda=9.5/s mu=10/s workers=1 timeout=9s retries=3",
		`headroom: "spike of 40/s for 10s"`,
	} {
		if !strings.Contains(rules, want) {
			t.Errorf("rules lack %q:\n%s", want, rules)
		}
	}

	size := func(r *qmeta.Region) int {
		n := 0
		for u := 0; u <= r.Chain.N; u++ {
			for v := 0; v <= r.Chain.V; v++ {
				if r.Contains(qmeta.State{U: u, V: v}) {
					n++
				}
			}
		}
		return n
	}
	if slow, storm := size(example(8).Region(healthy, 120)), size(r); slow*2 > storm {
		t.Errorf("at 8/s the region has %d states, at 9.5/s %d", slow, storm)
	}
	calm := example(8).Region(healthy, 3600)
	if len(calm.Bands(1)) != 0 {
		t.Errorf("at 8/s, bound 1h: %d bands", len(calm.Bands(1)))
	}
	if !strings.Contains(calm.Rules(qmeta.RuleConfig{Server: "api"}, "u <= 10"), "vector(0) > 1") {
		t.Error("at 8/s the rule can fire")
	}
}

// The backward pass agrees with the forward one.
func TestExpectAll(t *testing.T) {
	c := qmeta.New(qmeta.Params{Lambda: 9.5, Mu: 10, Timeout: 9, Retries: 3, Queue: 30, Orbit: 30})
	times := []float64{0, 5, 20}
	all := c.ExpectAll(times)
	for _, s := range []qmeta.State{{U: 0, V: 0}, {U: 30, V: 20}, {U: 12, V: 3}} {
		one := c.Expect(s, times)
		for i := range times {
			a := all[i][c.Index(s)]
			if d := max(a.U-one[i].U, one[i].U-a.U, a.V-one[i].V, one[i].V-a.V); d > 1e-6 {
				t.Errorf("from %+v at %gs: backward %+v, forward %+v", s, times[i], a, one[i])
			}
		}
	}
}

// One Healthy value is the region's predicate, the rules' description and
// the PromQL they annotate.
func TestHealthyRegion(t *testing.T) {
	c := qmeta.New(qmeta.Params{Lambda: 9.5, Mu: 10, Timeout: 9, Retries: 3, Queue: 30, Orbit: 30})
	h := qmeta.Healthy{MaxU: 10}
	r := c.HealthyRegion(h, 120)
	if !h.Contains(qmeta.State{U: 10}) || h.Contains(qmeta.State{U: 11}) {
		t.Error("Contains")
	}
	rules := r.Rules(qmeta.RuleConfig{Server: "api", Service: "api"}, "")
	for _, want := range []string{"of u <= 10 within", `healthy: "quint_serve_inflight{name=\"api\"} <= 10"`} {
		if !strings.Contains(rules, want) {
			t.Errorf("rules lack %q:\n%s", want, rules)
		}
	}
}
