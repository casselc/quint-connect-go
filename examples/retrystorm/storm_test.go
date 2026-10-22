package retrystorm

import (
	"github.com/casselc/quint-connect-go/quinthegel"
	"testing"
	"time"

	"github.com/casselc/quint-connect-go/qmeta"
	"hegel.dev/go/hegel"
)

// The CTMC tier: the budget drains in about a
// minute at every load; retrying at once takes orders of magnitude longer
// as the load grows, and backoff alone is worse still (it keeps rejected
// requests in the orbit, where retrying at once gives up).
func TestPredictedRecovery(t *testing.T) {
	at := map[string]map[float64]float64{}
	for _, p := range Policies {
		at[p.Name] = map[float64]float64{}
		for _, lam := range []float64{8, 9, 9.5} {
			at[p.Name][lam] = RecoveryTime(p, lam)
			t.Logf("%-15s λ=%.1f: recovery %.3g s", p.Name, lam, at[p.Name][lam])
		}
	}
	for _, lam := range []float64{8, 9, 9.5} {
		for _, p := range []string{"budget", "budget+backoff"} {
			if at[p][lam] > 600 {
				t.Errorf("%s at λ=%v: predicted recovery %.0f s, want under 10 minutes", p, lam, at[p][lam])
			}
		}
		if at["immediate"][lam] < 10*at["budget"][lam] || at["backoff"][lam] < at["immediate"][lam] {
			t.Errorf("λ=%v: predicted recovery immediate %.0f s, backoff %.0f s, budget %.0f s; want budget << immediate <= backoff",
				lam, at["immediate"][lam], at["backoff"][lam], at["budget"][lam])
		}
	}
}

// The simulation tier confirms the prediction at 9 requests/s: after the
// spike, runs with a budget recover; with backoff alone none do; retrying
// at once is a coin toss (the metastable region: whether a run escapes is
// a matter of chance).
func TestStormAfterSpike(t *testing.T) {
	const n, lam = 6, 9
	got := map[string]int{}
	for _, p := range Policies {
		got[p.Name] = Recoveries(p, lam, Spike, n)
		t.Logf("%-15s %d/%d runs recover after %v", p.Name, got[p.Name], n, Spike)
	}
	if got["budget"] < n-1 || got["budget+backoff"] < n-1 {
		t.Errorf("with a budget, %d and %d of %d runs recover; want all (one may be noise)", got["budget"], got["budget+backoff"], n)
	}
	if got["backoff"] > 1 {
		t.Errorf("with backoff alone, %d of %d runs recover; want none", got["backoff"], n)
	}
}

// Hegel searches the triggers and shrinks one that tips the system into a
// state it stays in: one exists for retrying at once (at 9 requests/s) and
// for backoff alone (at 8), none is found with a budget.
func TestFindTrigger(t *testing.T) {
	if testing.Short() {
		t.Skip("each trigger runs 3 simulations of ~10 minutes")
	}
	space := qmeta.TriggerSpace{MaxExtraRate: 10, MaxDuration: 5 * time.Minute}
	for _, c := range []struct {
		p     Policy
		lam   float64
		found bool
	}{{Policies[0], 9, true}, {Policies[1], 8, true}, {Policies[2], 9, false}, {Policies[3], 9, false}} {
		t.Run(c.p.Name, func(t *testing.T) {
			r := quinthegel.FindTrigger(t, c.p.Config(c.lam), c.lam, space, 5*time.Minute, hegel.WithTestCases(30))
			switch {
			case c.found && r == nil:
				t.Errorf("no trigger found at λ=%v; want one", c.lam)
			case !c.found && r != nil:
				t.Errorf("found %v at λ=%v; want none", r.Trigger, c.lam)
			}
		})
	}
}
