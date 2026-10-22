package checkout_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/casselc/quint-connect-go/examples/checkout"
	"github.com/casselc/quint-connect-go/examples/checkout/shop"
	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qobs"
	"github.com/casselc/quint-connect-go/qtest"
	"github.com/casselc/quint-connect-go/quinthegel"
	"hegel.dev/go/hegel"
)

const binding = "checkout.binding.yaml"

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
		want  []qtest.Claim
	}{
		{shop.NoLocks, qenv.Serializable, crashy, []qtest.Claim{qtest.Conforms()}},
		{shop.NoLocks, qenv.SnapshotIsolation, crashy, []qtest.Claim{qtest.Conforms()}},
		// lost updates: the stock oversold, or a shipment and a cancellation
		{shop.NoLocks, qenv.ReadCommitted, qenv.DefaultRates, []qtest.Claim{qtest.AnyOf(qtest.Rejects("place"), shipOrCancel)}},
		{shop.LockWithoutWrite, qenv.Serializable, crashy, []qtest.Claim{qtest.Conforms()}},
		{shop.LockWithoutWrite, qenv.ReadCommitted, crashy, []qtest.Claim{qtest.Conforms()}},
		// write skew: a shipment and a cancellation both commit
		{shop.LockWithoutWrite, qenv.SnapshotIsolation, qenv.DefaultRates, []qtest.Claim{shipOrCancel}},
		// a retry after a lost response charges again
		{shop.NoIdempotencyKey, qenv.Serializable, qenv.DefaultRates, []qtest.Claim{qtest.Violates("chargedAtMostOnce")}},
		// a crash between commit and publish loses the order's payment: a
		// one-step window, found with crashes ten times as often
		{shop.DualWrite, qenv.Serializable, crashy, []qtest.Claim{qtest.Violates("settledCorrectly")}},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%d/%s", c.m, c.iso), func(t *testing.T) {
			explore(t, checkout.Config{Isolation: c.iso, Mutation: c.m}, qtest.ExploreConfig{Seeds: 100, Rates: c.rates}).Expect(t, c.want...)
		})
	}
}

// Hegel makes every choice of the environment and shrinks a write skew to
// the interleaving and faults it needs. The shrunk run's choices replay it
// outside Hegel, step for step: a regression test needs no Hegel.
func TestHegelShrinksWriteSkew(t *testing.T) {
	qtest.RequireQuint(t)
	if testing.Short() {
		t.Skip("shrinking against the model")
	}
	v := quinthegel.FindViolation(t, quinthegel.Config{Binding: binding}, func(tc hegel.TestCase, rec *quinthegel.Recorder) {
		c := rec.Env(quinthegel.Chooser(tc)) // the run records its steps and choices into rec
		if _, err := checkout.Run(c, checkout.Config{Isolation: qenv.SnapshotIsolation, Mutation: shop.LockWithoutWrite}); err != nil {
			tc.Errorf("%v", err)
		}
	}, hegel.WithTestCases(50), hegel.WithSeed(1))
	v.Expect(t, qtest.AnyOf(qtest.Rejects("ship"), qtest.Rejects("cancel")))

	mem := &qobs.MemorySink{}
	if _, err := checkout.Run(qenv.WithContext(qenv.Replay(v.Choices), qobs.WithRecorder(context.Background(), qobs.NewRecorder(mem))),
		checkout.Config{Isolation: qenv.SnapshotIsolation, Mutation: shop.LockWithoutWrite}); err != nil {
		t.Fatal(err)
	}
	if got, want := quinthegel.Render(mem.Steps()), quinthegel.Render(v.Steps); got != want {
		t.Errorf("the replayed run differs:\n%s\nfrom the shrunk run:\n%s", got, want)
	}
	qtest.Validate(t, binding, mem.Steps()).Expect(t, qtest.AnyOf(qtest.Rejects("ship"), qtest.Rejects("cancel")))
}

// Hegel explores the faithful shop under snapshot isolation, in batches.
func TestHegelFaithful(t *testing.T) {
	qtest.RequireQuint(t)
	quinthegel.Test(t, quinthegel.Config{Binding: binding, Mode: quinthegel.Batched}, func(ht *hegel.T, rec *quinthegel.Recorder) {
		if _, err := checkout.Run(rec.Env(quinthegel.Chooser(ht)), checkout.Config{Isolation: qenv.SnapshotIsolation}); err != nil {
			ht.Fatalf("%v", err)
		}
	}, hegel.WithTestCases(30))
}
