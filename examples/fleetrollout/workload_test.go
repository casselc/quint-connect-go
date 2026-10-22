package fleetrollout_test

import (
	"testing"

	"github.com/casselc/quint-connect-go/examples/fleetrollout"
	"github.com/casselc/quint-connect-go/examples/fleetrollout/rollout"
	"github.com/casselc/quint-connect-go/qtest"
	"github.com/casselc/quint-connect-go/sktrace"
)

// production is SimKube's scaling trace: a real cluster's Deployments
// scaled up and down over 19 minutes.
func production(t *testing.T) *sktrace.Trace {
	t.Helper()
	tr, err := sktrace.ReadFile("../../sktrace/testdata/dsb-scaling.sktrace")
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

// The rollout with a production workload as another writer: the trace's
// scalings land on the members' Deployments (60 times faster) while the
// rollout patches them, through the same faults. The model needed a step
// for another writer (scale); with it, the faithful controller keeps the
// budget and finishes, and trusting a stale status is still caught.
func TestRolloutUnderProductionWorkload(t *testing.T) {
	qtest.RequireQuint(t)
	tr := production(t)
	x := explore(t, fleetrollout.Config{Workload: tr}, 30)
	x.Expect(t, qtest.Conforms())
	scales := 0
	for _, r := range x.Runs {
		for _, s := range r.Steps {
			if s.Action == "scale" {
				scales++
			}
		}
	}
	if scales < len(x.Runs) {
		t.Errorf("%d scale steps over %d runs: the workload did not reach the fleet", scales, len(x.Runs))
	}
	explore(t, fleetrollout.Config{Workload: tr, Mutation: rollout.IgnoreObservedGeneration, NoCrashes: true}, 20).
		Expect(t, qtest.Violates("withinBudget"))
}
