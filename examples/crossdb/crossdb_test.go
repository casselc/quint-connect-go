package crossdb_test

import (
	"testing"

	"github.com/casselc/quint-connect-go/examples/crossdb"
	"github.com/casselc/quint-connect-go/examples/crossdb/provider"
	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qtest"
	"github.com/casselc/quint-connect-go/quinthegel"
	"hegel.dev/go/hegel"
)

const binding = "crossdb.binding.yaml"

func explore(t *testing.T, cfg crossdb.Config, x qtest.ExploreConfig) *qtest.Exploration {
	return qtest.System{Binding: binding, Run: func(c qenv.Chooser) error { return crossdb.Run(c, cfg) }}.Explore(t, x)
}

// crashy crashes the provider at one scheduling step in fifty.
var crashy = qenv.Rates{Default: 0.05, Kinds: map[string]float64{"fault.nemesis": 0.02}}

// Through lost and duplicated requests, lost responses, throttling, an
// eventually consistent cloud and provider crashes, the faithful provider
// makes one database per resource, deletes them all, and writes secrets
// that open them.
func TestFaithfulConforms(t *testing.T) {
	qtest.RequireQuint(t)
	explore(t, crossdb.Config{}, qtest.ExploreConfig{Seeds: 60, Rates: crashy}).Expect(t, qtest.Conforms())
}

// The cloud's ID as the external name. Correct while nothing fails. A
// create whose response is lost is marked failed (by Crossplane's
// contract, an error from Create is a failure); if the resource is deleted
// before the retry, there is no name to look up, the finalizer comes off,
// and the database leaks. A crash between the create and saving the name
// is worse: Crossplane refuses to go on.
func TestGeneratedExternalName(t *testing.T) {
	qtest.RequireQuint(t)
	noFaults := qenv.Rates{Default: 0, Kinds: map[string]float64{"fault": 0}}
	explore(t, crossdb.Config{Mutation: provider.GeneratedExternalName, NoCrashes: true}, qtest.ExploreConfig{Seeds: 20, Rates: noFaults}).
		Expect(t, qtest.Conforms())
	explore(t, crossdb.Config{Mutation: provider.GeneratedExternalName, NoCrashes: true}, qtest.ExploreConfig{Seeds: 40}).
		Expect(t, qtest.Violates("settledCorrectly"))
	// crashes alone: an incomplete create, and a resource that never gets ready
	crashesOnly := qenv.Rates{Kinds: map[string]float64{"fault": 0, "fault.nemesis": 0.05}}
	explore(t, crossdb.Config{Mutation: provider.GeneratedExternalName}, qtest.ExploreConfig{Seeds: 40, Rates: crashesOnly}).
		Expect(t, qtest.Violates("settledCorrectly"))
}

// Without a client token, a create whose response was lost is retried as a
// failure: a second database.
func TestNoClientTokenDuplicates(t *testing.T) {
	qtest.RequireQuint(t)
	explore(t, crossdb.Config{Mutation: provider.NoClientToken, NoCrashes: true}, qtest.ExploreConfig{Seeds: 40}).
		Expect(t, qtest.Violates("oneDatabasePerResource"))
}

// A password generated in Create and published only with the creation's
// result: a lost response (the retry's new password is ignored by the
// idempotent cloud) leaves a secret that does not open the database.
func TestPasswordFromCreate(t *testing.T) {
	qtest.RequireQuint(t)
	lossy := qenv.Rates{Default: 0.05, Kinds: map[string]float64{"fault.rpc.dbaas.response": 0.3}}
	explore(t, crossdb.Config{Mutation: provider.PasswordFromCreate, NoCrashes: true}, qtest.ExploreConfig{Seeds: 40, Rates: lossy}).
		Expect(t, qtest.Violates("settledCorrectly"))
}

// Hegel shrinks the duplicate to the fault it needs: one lost response.
func TestHegelShrinksDuplicate(t *testing.T) {
	qtest.RequireQuint(t)
	if testing.Short() {
		t.Skip("shrinking against the model")
	}
	quinthegel.FindViolation(t, quinthegel.Config{Binding: binding, Global: true}, func(tc hegel.TestCase, _ *quinthegel.Recorder) {
		if err := crossdb.Run(quinthegel.Chooser(tc), crossdb.Config{Mutation: provider.NoClientToken, NoCrashes: true, Resources: 2}); err != nil {
			tc.Errorf("%v", err)
		}
	}, hegel.WithTestCases(50), hegel.WithSeed(1))
}
