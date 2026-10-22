package checkout_test

import (
	"context"
	"testing"

	"github.com/casselc/quint-connect-go/examples/checkout"
	"github.com/casselc/quint-connect-go/examples/checkout/shop"
	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qobs"
	"github.com/casselc/quint-connect-go/qsweep"
	"github.com/casselc/quint-connect-go/qtest"
)

// The shop's parameter space: isolation level, orders and stock, and which
// kinds of faults happen at all. 432 combinations in full.
var space = []qsweep.Factor{
	{Name: "iso", Values: []string{"ser", "si", "rc"}},
	qsweep.Ints("orders", 2, 3, 4),
	qsweep.Ints("stock", 1, 2, 3),
	qsweep.Bool("crash"),
	qsweep.Bool("queue.ack"),
	qsweep.Bool("rpc.api"),
	qsweep.Bool("rpc.provider"),
}

var isoOf = map[string]qenv.Isolation{"ser": qenv.Serializable, "si": qenv.SnapshotIsolation, "rc": qenv.ReadCommitted}

// stockOf checks a row against the model with its stock.
func stockOf(row qsweep.Row) []qtest.Option {
	return []qtest.Option{qtest.Constants(map[string]string{"STOCK": row.Get("stock")})}
}

// deploy runs the shop as a row says, with the fault kinds it turns off.
func deploy(m shop.Mutation) func(qsweep.Row, qenv.Chooser) error {
	return func(row qsweep.Row, c qenv.Chooser) error {
		var off []string
		for _, k := range []string{"queue.ack", "rpc.api", "rpc.provider"} {
			if !row.On(k) {
				off = append(off, "fault."+k)
			}
		}
		cfg := checkout.Config{Isolation: isoOf[row.Get("iso")], Mutation: m,
			Orders: row.Int("orders"), Stock: row.Int("stock"), NoCrashes: !row.On("crash")}
		_, err := checkout.Run(qenv.Without(c, off...), cfg)
		return err
	}
}

// shopSystem is the shop bound to its model, for sweeps (each row says how
// to deploy it).
var shopSystem = qtest.System{Binding: binding}

// The faithful shop conforms on every pairwise row: every pair of factor
// values, every pair of fault kinds together and each kind alone, at
// every isolation level.
func TestFaithfulSweep(t *testing.T) {
	qtest.RequireQuint(t)
	if testing.Short() {
		t.Skip("sweeps hundreds of runs")
	}
	sw := shopSystem.Sweep(t, qtest.Sweep{Factors: space, Opts: stockOf, Explore: qtest.ExploreConfig{Seeds: 20, Rates: crashy}}, deploy(shop.Faithful))
	cov := sw.Coverage()
	t.Log(cov)
	if u := cov.Untried(); len(u) > 0 {
		t.Errorf("fault kinds never together: %v", u)
	}
}

// A fresh idempotency key per attempt is two bugs, and the sweep tells
// them apart. A cancellation's void cannot find the charge in every row,
// faults or none (settledCorrectly). A double charge needs the provider's
// faults: some run of every row with them charges twice, and no run of a
// row without them does.
func TestNoIdempotencyKeySweep(t *testing.T) {
	qtest.RequireQuint(t)
	if testing.Short() {
		t.Skip("sweeps hundreds of runs")
	}
	shopSystem.Sweep(t, qtest.Sweep{Factors: space, Opts: stockOf, Explore: qtest.ExploreConfig{Seeds: 20, Rates: crashy},
		Expect: func(row qsweep.Row) []qtest.Claim {
			if row.On("rpc.provider") {
				return []qtest.Claim{qtest.Violates("chargedAtMostOnce")}
			}
			return []qtest.Claim{qtest.Violates("settledCorrectly"), qtest.None(qtest.Violates("chargedAtMostOnce"))}
		}}, deploy(shop.NoIdempotencyKey))
}

// Both orders of every pair of fault kinds, each imposed by a Script on a
// seeded schedule (up to three seeds per order), checked by the model. A
// whole sequence covering array (qsweep.Sequences) does not fit the shop:
// its API faults happen only while orders are placed, so a strict order of
// all six kinds never completes. Pairs do, except the orders the shop's
// phases rule out, which the test names.
func TestFaultOrders(t *testing.T) {
	qtest.RequireQuint(t)
	if testing.Short() {
		t.Skip("sweeps hundreds of runs")
	}
	kinds := []string{"nemesis.crash.payments", "queue.ack", "rpc.api.request", "rpc.api.response", "rpc.provider.request", "rpc.provider.response"}
	var runs [][]qenv.Choice
	var impossible []string
	for _, a := range kinds {
		for _, b := range kinds {
			if a == b {
				continue
			}
			done := false
			for seed := uint64(1); seed <= 3 && !done; seed++ {
				sc := &qenv.Script{Base: qenv.NewSeeded(seed), Faults: []qenv.Scripted{{Kind: a, Option: 1}, {Kind: b, Option: 1}}}
				rec := &qenv.Recorder{C: sc}
				mem := &qobs.MemorySink{}
				if _, err := checkout.Run(qenv.WithContext(rec, qobs.WithRecorder(context.Background(), qobs.NewRecorder(mem))), checkout.Config{Isolation: qenv.SnapshotIsolation}); err != nil {
					t.Fatal(err)
				}
				qtest.Check(t, binding, mem.Steps())
				runs = append(runs, rec.Choices)
				done = len(sc.Remaining()) == 0
			}
			if !done {
				impossible = append(impossible, a+" then "+b)
			}
		}
	}
	t.Logf("orders the shop never produced: %v", impossible)
	cov := qenv.CoverageOf(runs)
	t.Log(cov)
	if len(cov.Untried()) > 0 {
		t.Errorf("pairs never together: %+v", cov.Untried())
	}
}
