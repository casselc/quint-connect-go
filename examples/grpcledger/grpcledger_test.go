package grpcledger_test

import (
	"testing"

	"hegel.dev/go/hegel"

	"github.com/casselc/quint-connect-go/examples/grpcledger"
	"github.com/casselc/quint-connect-go/examples/grpcledger/ledger"
	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qsweep"
	"github.com/casselc/quint-connect-go/qtest"
	"github.com/casselc/quint-connect-go/quinthegel"
)

const binding = "ledger.binding.yaml"

// system is the ledger as cfg deploys it, bound to the model.
func system(cfg grpcledger.Config) qtest.System {
	return qtest.System{Binding: binding, Run: func(c qenv.Chooser) error {
		_, err := grpcledger.Run(c, cfg)
		return err
	}}
}

// seeds is n, or a fifth of it with -short: the same verdicts on fewer
// runs (the seeds are fixed, so the short run is a deterministic subset).
func seeds(n int) int {
	if testing.Short() {
		return n / 5
	}
	return n
}

// noFaults schedules at random but injects nothing.
var noFaults = qenv.Rates{Kinds: map[string]float64{}}

// crashy crashes the server at one scheduling step in fifty, on top of the
// network's faults.
var crashy = qenv.Rates{Default: 0.05, Kinds: map[string]float64{"fault.nemesis": 0.02}}

// The faithful ledger conforms through refused connections, lost and
// duplicated requests, lost responses and server crashes: every retry is
// answered once.
func TestFaithfulConforms(t *testing.T) {
	qtest.RequireQuint(t)
	x := system(grpcledger.Config{}).Explore(t, qtest.ExploreConfig{Seeds: seeds(60), Rates: crashy})
	x.Expect(t, qtest.Conforms())
	t.Log(x.Evidence())
}

// reapplied is the missing dedupe's signature: a request whose key was
// applied runs again, and either moves the money again (a transfer the
// model cannot take) or, the funds now gone, is refused (a refusal of a key
// the model has applied). Hegel shrinks to the second: one duplicated
// request of the whole balance.
var reapplied = qtest.AnyOf(qtest.Rejects("transfer"), qtest.Rejects("refuse"))

// Which check catches which bug, and whether faults are needed to see it.
func TestMutants(t *testing.T) {
	qtest.RequireQuint(t)
	cases := []struct {
		m     ledger.Mutation
		rates qenv.Rates
		want  qtest.Claim
	}{
		// without faults every key arrives once: the missing dedupe hides
		{ledger.NoDedupe, noFaults, qtest.Conforms()},
		// a retry after a lost response (or a duplicated request) runs the
		// key again
		{ledger.NoDedupe, qenv.DefaultRates, reapplied},
		// an overdraft needs no fault, only a client asking for it
		{ledger.NoFundsCheck, noFaults, qtest.Rejects("transfer")},
		// a lost update needs only an interleaving: the balances a
		// transfer leaves are not the model's
		{ledger.SplitTxn, noFaults, qtest.Mismatch("transfer")},
	}
	for _, c := range cases {
		name := c.m.String() + "/faults"
		if c.rates.Default == 0 {
			name = c.m.String() + "/nofaults"
		}
		t.Run(name, func(t *testing.T) {
			system(grpcledger.Config{Mutation: c.m}).Explore(t, qtest.ExploreConfig{Seeds: seeds(40), Rates: c.rates}).Expect(t, c.want)
		})
	}
}

// Which faults make the missing dedupe a bug, pairwise over the fault
// kinds: a duplicated request or a lost response. A refused connection is
// harmless (nothing was sent), and so, in the simulation, is a crash: the
// handler's commit, return and response are one scheduling step, so no
// crash falls between the commit and the response. A real server can die
// there; the lost response is how the simulation has it.
func TestNoDedupeSweep(t *testing.T) {
	qtest.RequireQuint(t)
	if testing.Short() {
		t.Skip("sweeps hundreds of runs")
	}
	kinds := map[string]string{"conn": "fault.grpc.ledger.conn", "request": "fault.rpc.ledger.request", "response": "fault.rpc.ledger.response"}
	space := []qsweep.Factor{qsweep.Bool("conn"), qsweep.Bool("request"), qsweep.Bool("response"), qsweep.Bool("crash")}
	sw := system(grpcledger.Config{Mutation: ledger.NoDedupe}).Sweep(t, qtest.Sweep{Factors: space,
		Explore: qtest.ExploreConfig{Seeds: 30, Rates: qenv.Rates{Default: 0.1, Kinds: map[string]float64{"fault.nemesis": 0.02}}},
		Expect: func(row qsweep.Row) []qtest.Claim {
			if row.On("request") || row.On("response") {
				return []qtest.Claim{reapplied}
			}
			return []qtest.Claim{qtest.Conforms()}
		}}, func(row qsweep.Row, c qenv.Chooser) error {
		var off []string
		for f, k := range kinds {
			if !row.On(f) {
				off = append(off, k)
			}
		}
		_, err := grpcledger.Run(qenv.Without(c, off...), grpcledger.Config{Mutation: ledger.NoDedupe, NoCrashes: !row.On("crash")})
		return err
	})
	t.Log(sw.Evidence())
}

// Hegel makes every choice and shrinks the double-applied transfer to the
// fault it needs; the shrunk run's choices replay it without Hegel.
func TestShrinkDoubleApply(t *testing.T) {
	qtest.RequireQuint(t)
	if testing.Short() {
		t.Skip("shrinks against the model")
	}
	v := quinthegel.Shrink(t, system(grpcledger.Config{Mutation: ledger.NoDedupe}), hegel.WithTestCases(200))
	v.Expect(t, reapplied)
	t.Logf("shrunk: %d steps, faults %v\n%s", len(v.Steps), (&qenv.Recorder{Choices: v.Choices}).Faults(), v.Summary)
}
