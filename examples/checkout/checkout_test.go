package checkout_test

import (
	"context"
	"fmt"
	"regexp"
	"testing"

	"github.com/casselc/quint-connect-go/examples/checkout"
	"github.com/casselc/quint-connect-go/examples/checkout/shop"
	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qtest"
	"github.com/casselc/quint-connect-go/quinthegel"
	"hegel.dev/go/hegel"
)

const binding = "checkout.binding.yaml"

// rejected matches a report of a shipment or cancellation the model cannot take.
var rejected = regexp.MustCompile(`is not a transition[^\n]*\n\s+quint:\s+(ship|cancel)\(`)

func explore(t *testing.T, cfg checkout.Config, x qtest.ExploreConfig) *qtest.Exploration {
	return qtest.Explore(t, binding, x, func(c qenv.Chooser) {
		if _, err := checkout.Run(c, cfg); err != nil {
			t.Fatal(err)
		}
	})
}

// crashy crashes a process at one scheduling step in twenty.
var crashy = qenv.Rates{Default: 0.05, Kinds: map[string]float64{"fault.nemesis": 0.05}}

var levels = []qenv.Isolation{qenv.Serializable, qenv.SnapshotIsolation, qenv.ReadCommitted}

// The faithful shop is correct at every isolation level, through crashes,
// lost responses, duplicate requests and redeliveries.
func TestFaithfulConformsAtEveryLevel(t *testing.T) {
	qtest.RequireQuint(t)
	for _, iso := range levels {
		t.Run(iso.String(), func(t *testing.T) {
			explore(t, checkout.Config{Isolation: iso}, qtest.ExploreConfig{Seeds: 60, Rates: crashy}).Expect(t, qtest.Conforms())
		})
	}
}

// What each bug looks like, and at which isolation levels it is a bug at
// all: missing row locks only matter under read committed, a lock without
// a write only under snapshot isolation.
func TestMutants(t *testing.T) {
	qtest.RequireQuint(t)
	shipOrCancel := qtest.AnyOf(qtest.Rejects("ship"), qtest.Rejects("cancel"))
	cases := []struct {
		m     shop.Mutation
		iso   qenv.Isolation
		rates qenv.Rates
		want  []qtest.Expectation
	}{
		{shop.NoLocks, qenv.Serializable, crashy, []qtest.Expectation{qtest.Conforms()}},
		{shop.NoLocks, qenv.SnapshotIsolation, crashy, []qtest.Expectation{qtest.Conforms()}},
		// lost updates: the stock oversold, or a shipment and a cancellation
		{shop.NoLocks, qenv.ReadCommitted, qenv.DefaultRates, []qtest.Expectation{qtest.AnyOf(qtest.Rejects("place"), shipOrCancel)}},
		{shop.LockWithoutWrite, qenv.Serializable, crashy, []qtest.Expectation{qtest.Conforms()}},
		{shop.LockWithoutWrite, qenv.ReadCommitted, crashy, []qtest.Expectation{qtest.Conforms()}},
		// write skew: a shipment and a cancellation both commit
		{shop.LockWithoutWrite, qenv.SnapshotIsolation, qenv.DefaultRates, []qtest.Expectation{shipOrCancel}},
		// a retry after a lost response charges again
		{shop.NoIdempotencyKey, qenv.Serializable, qenv.DefaultRates, []qtest.Expectation{qtest.Violates("chargedAtMostOnce")}},
		// a crash between commit and publish loses the order's payment: a
		// one-step window, found with crashes ten times as often
		{shop.DualWrite, qenv.Serializable, crashy, []qtest.Expectation{qtest.Violates("settledCorrectly")}},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%d/%s", c.m, c.iso), func(t *testing.T) {
			explore(t, checkout.Config{Isolation: c.iso, Mutation: c.m}, qtest.ExploreConfig{Seeds: 100, Rates: c.rates}).Expect(t, c.want...)
		})
	}
}

// Hegel makes every choice of the environment and shrinks a write skew to
// the interleaving and faults it needs.
func TestHegelShrinksWriteSkew(t *testing.T) {
	qtest.RequireQuint(t)
	if testing.Short() {
		t.Skip("shrinking against the model")
	}
	v := quinthegel.FindViolation(t, quinthegel.Config{Binding: binding}, func(tc hegel.TestCase, rec *quinthegel.Recorder) {
		c := qenv.WithContext(qenv.Hegel(tc), rec.Context(context.Background())) // the run records into rec
		if _, err := checkout.Run(c, checkout.Config{Isolation: qenv.SnapshotIsolation, Mutation: shop.LockWithoutWrite}); err != nil {
			tc.Errorf("%v", err)
		}
	}, hegel.WithTestCases(50), hegel.WithSeed(1))
	if !rejected.MatchString(v.Summary) {
		t.Fatalf("want a shipment or cancellation the model rejects:\n%s", v.Summary)
	}
}

// Hegel explores the faithful shop under snapshot isolation, in batches.
func TestHegelFaithful(t *testing.T) {
	qtest.RequireQuint(t)
	quinthegel.Test(t, quinthegel.Config{Binding: binding, Mode: quinthegel.Batched}, func(ht *hegel.T, rec *quinthegel.Recorder) {
		if _, err := checkout.Run(qenv.WithContext(qenv.Hegel(ht), rec.Context(context.Background())), checkout.Config{Isolation: qenv.SnapshotIsolation}); err != nil {
			ht.Fatalf("%v", err)
		}
	}, hegel.WithTestCases(30))
}
