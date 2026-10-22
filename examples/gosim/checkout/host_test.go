//go:build !sim

// The checkout saga's model checks, on gosim: the same deployment, model,
// binding and expected verdicts as ../../checkout/checkout_test.go, with
// the deployment on qenv's native runtime in gosim (sim_test.go) instead of
// qenv's scheduler. Each seed is a gosim run whose choices the seeded
// Chooser makes, explored in parallel by qtest.Explore over a pool of
// test binaries.
package checkout_test

import (
	"fmt"
	"testing"

	"github.com/casselc/quint-connect-go/examples/checkout/shop"
	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qgosim"
	"github.com/casselc/quint-connect-go/qtest"
)

const (
	pkg     = "github.com/casselc/quint-connect-go/examples/gosim/checkout"
	binding = "../../checkout/checkout.binding.yaml"
)

func explore(t *testing.T, iso qenv.Isolation, m shop.Mutation, x qtest.ExploreConfig) *qtest.Exploration {
	sys := qtest.System{Binding: binding, Run: qgosim.Scenario(t, pkg, "TestCheckout",
		fmt.Sprintf("CHECKOUT_ISOLATION=%d", iso), fmt.Sprintf("CHECKOUT_MUTATION=%d", m))}
	return sys.Explore(t, x)
}

// crashy crashes a process at one nemesis choice in twenty (one every
// 100ms of simulated time on the native runtime).
var crashy = qenv.Rates{Default: 0.05, Kinds: map[string]float64{"nemesis": 0.05}}

// The faithful shop conforms at every isolation level, through crashes of
// whole machines, lost responses, duplicates and redeliveries.
func TestFaithfulConformsOnGosim(t *testing.T) {
	qtest.RequireQuint(t)
	for _, iso := range []qenv.Isolation{qenv.Serializable, qenv.SnapshotIsolation, qenv.ReadCommitted} {
		t.Run(iso.String(), func(t *testing.T) {
			explore(t, iso, shop.Faithful, qtest.ExploreConfig{Seeds: 8, Rates: crashy}).Expect(t, qtest.Conforms())
		})
	}
}

// The mutants qenv's scheduler catches, caught on gosim too.
func TestMutantsOnGosim(t *testing.T) {
	qtest.RequireQuint(t)
	shipOrCancel := qtest.AnyOf(qtest.Rejects("ship"), qtest.Rejects("cancel"))
	cases := []struct {
		m     shop.Mutation
		iso   qenv.Isolation
		rates qenv.Rates
		want  qtest.Expectation
	}{
		// write skew: a shipment and a cancellation both commit
		{shop.LockWithoutWrite, qenv.SnapshotIsolation, qenv.DefaultRates, shipOrCancel},
		// Fresh keys per attempt: a retry after a lost response charges
		// again, and a cancellation's void (by the order's key) finds no
		// charge to refund. qenv's scheduler shows the first; gosim's
		// schedules, where a cancel races the payment, mostly the second.
		{shop.NoIdempotencyKey, qenv.Serializable, qenv.DefaultRates,
			qtest.AnyOf(qtest.Violates("chargedAtMostOnce"), qtest.Violates("settledCorrectly"))},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%d/%s", c.m, c.iso), func(t *testing.T) {
			explore(t, c.iso, c.m, qtest.ExploreConfig{Seeds: 30, Rates: c.rates}).Expect(t, c.want)
		})
	}
}
