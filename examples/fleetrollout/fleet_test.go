package fleetrollout_test

import (
	"testing"

	"github.com/casselc/quint-connect-go/examples/fleetrollout"
	"github.com/casselc/quint-connect-go/examples/fleetrollout/rollout"
	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qtest"
	"github.com/casselc/quint-connect-go/quinthegel"
	"hegel.dev/go/hegel"
)

const binding = "fleet.binding.yaml"

// system is the controller in a simulated fleet, configured as cfg.
func system(cfg fleetrollout.Config) qtest.System {
	return qtest.System{Binding: binding, Run: func(c qenv.Chooser) error {
		_, err := fleetrollout.Run(c, cfg)
		return err
	}}
}

func explore(t *testing.T, cfg fleetrollout.Config, seeds int) *qtest.Exploration {
	return system(cfg).Explore(t, qtest.ExploreConfig{Seeds: seeds})
}

// The faithful controller keeps the budget and finishes the rollout,
// through lost requests and responses, status lag and its own crashes.
func TestFaithfulConforms(t *testing.T) {
	qtest.RequireQuint(t)
	explore(t, fleetrollout.Config{}, 30).Expect(t, qtest.Conforms())
}

// Trusting a status written for the previous generation: the next
// cluster is patched while the last one has barely started.
func TestIgnoreObservedGenerationOverBudget(t *testing.T) {
	qtest.RequireQuint(t)
	explore(t, fleetrollout.Config{Mutation: rollout.IgnoreObservedGeneration, NoCrashes: true}, 20).
		Expect(t, qtest.Violates("withinBudget"))
}

// Progress kept in memory: correct while nothing fails. A restart forgets
// it, and so does a patch whose response was lost (applied, but the
// controller never knew): either puts the rollout over budget.
func TestInMemoryProgress(t *testing.T) {
	qtest.RequireQuint(t)
	noKubeFaults := qenv.Rates{Default: 0.05, Kinds: map[string]float64{"fault.kube": 0}}
	system(fleetrollout.Config{Mutation: rollout.InMemoryProgress, NoCrashes: true}).
		Explore(t, qtest.ExploreConfig{Seeds: 15, Rates: noKubeFaults}).Expect(t, qtest.Conforms())
	system(fleetrollout.Config{Mutation: rollout.InMemoryProgress}).
		Explore(t, qtest.ExploreConfig{Seeds: 20, Rates: noKubeFaults}).Expect(t, qtest.Violates("withinBudget"))
	explore(t, fleetrollout.Config{Mutation: rollout.InMemoryProgress, NoCrashes: true}, 20).Expect(t, qtest.Violates("withinBudget"))
}

// Hegel shrinks the restart bug to the faults it needs: a crash.
func TestHegelShrinksInMemoryProgress(t *testing.T) {
	qtest.RequireQuint(t)
	if testing.Short() {
		t.Skip("shrinking against the model")
	}
	quinthegel.FindViolation(t, quinthegel.Config{Binding: binding, Global: true}, func(tc hegel.TestCase, _ *quinthegel.Recorder) {
		c := quinthegel.Chooser(tc)
		c.Rates.Kinds = map[string]float64{"fault.nemesis": 0.05}
		if _, err := fleetrollout.Run(c, fleetrollout.Config{Mutation: rollout.InMemoryProgress}); err != nil {
			tc.Errorf("%v", err)
		}
	}, hegel.WithTestCases(50), hegel.WithSeed(1))
}
