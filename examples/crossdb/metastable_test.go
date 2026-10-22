package crossdb

import (
	"github.com/casselc/quint-connect-go/quinthegel"
	"testing"
	"time"

	"github.com/casselc/quint-connect-go/qmeta"
	"hegel.dev/go/hegel"
)

// Dip is the trigger: the cloud API serves three times slower for a
// minute, ten minutes into the run (the closed loop's steady state).
var Dip = qmeta.Trigger{Start: 10 * time.Minute, Slowdown: 3, Duration: time.Minute}

func recoveries(r Requeue, tr qmeta.Trigger, n int) int {
	return qmeta.Count(qmeta.OfSimulation(DefaultControlPlane(r).Config(), 0, 5*time.Minute), tr, n)
}

// The performance tier of the provider: 600 Databases polled every
// minute, against a cloud API serving 20 calls/s, through a capacity dip.
// crossplane-runtime's requeue (per-item backoff to a minute, a global
// limit of 10 reconciles/s) recovers, and either half alone suffices. A
// controller that requeues failures after a fixed second does not: each
// failing object comes back every 3 s (timeout plus requeue), 200 calls/s
// against 20, and the storm outlives the dip. The global limit alone
// saves it. The CTMC agrees.
func TestControlPlaneRecovers(t *testing.T) {
	const n = 4
	for _, r := range Requeues {
		got := recoveries(r, Dip, n)
		c := qmeta.New(DefaultControlPlane(r).Params())
		h := c.HittingTimes(func(s qmeta.State) bool { return s.U <= 10 })
		pred := h[c.Index(qmeta.State{U: 100, V: 30})]
		t.Logf("%-32s %d/%d runs recover after %v; predicted recovery %.3g s", r.Name, got, n, Dip, pred)
		storm := r.Name == FixedRequeue.Name
		switch {
		case storm && got > 1:
			t.Errorf("%s: %d of %d runs recover; want a storm", r.Name, got, n)
		case !storm && got < n-1:
			t.Errorf("%s: %d of %d runs recover; want all (one may be noise)", r.Name, got, n)
		case storm && pred < 1e6, !storm && pred > 600:
			t.Errorf("%s: predicted recovery %.3g s", r.Name, pred)
		}
	}
}

// Hegel searches capacity dips (up to 4x slower for up to 5 minutes): one
// tips the fixed requeue into a storm; none tips crossplane's defaults.
func TestControlPlaneTrigger(t *testing.T) {
	if testing.Short() {
		t.Skip("each trigger runs 3 simulations of ~20 minutes")
	}
	space := qmeta.TriggerSpace{Start: 10 * time.Minute, MaxSlowdown: 4, MaxDuration: 5 * time.Minute}
	for _, c := range []struct {
		r     Requeue
		found bool
	}{{FixedRequeue, true}, {CrossplaneDefaults, false}} {
		t.Run(c.r.Name, func(t *testing.T) {
			r := quinthegel.FindTrigger(t, DefaultControlPlane(c.r).Config(), 0, space, 5*time.Minute, hegel.WithTestCases(25))
			switch {
			case c.found && r == nil:
				t.Error("no dip found; want one")
			case !c.found && r != nil:
				t.Errorf("found %v; want none", r.Trigger)
			}
		})
	}
}
