package bank

import (
	"context"
	"errors"
	"testing"

	"github.com/casselc/quint-connect-go/connect"
	"github.com/casselc/quint-connect-go/qtrace"
	"github.com/casselc/quint-connect-go/quinthegel"
	"github.com/casselc/quint-connect-go/validate"
	"hegel.dev/go/hegel"
)

// machine is a Hegel state machine over the bank. Its rules call the
// bank's API with drawn arguments (invalid ones included); the bank's own
// qobs.Record calls report the model steps; quinthegel checks the run.
type machine struct{ bank *Bank }

var (
	anyAccount = hegel.SampledFrom([]string{"alice", "bob", "carol", "mallory"}) // mallory has no account
	anyAmount  = hegel.Integers(-1, 6)
)

func (m *machine) RuleDeposit(tc hegel.TestCase) {
	_ = m.bank.Deposit(context.Background(), hegel.Draw(tc, anyAccount), hegel.Draw(tc, anyAmount))
}

func (m *machine) RuleWithdraw(tc hegel.TestCase) {
	_ = m.bank.Withdraw(context.Background(), hegel.Draw(tc, anyAccount), hegel.Draw(tc, anyAmount))
}

func (m *machine) RuleTransfer(tc hegel.TestCase) {
	_ = m.bank.Transfer(context.Background(), hegel.Draw(tc, anyAccount), hegel.Draw(tc, anyAccount), hegel.Draw(tc, anyAmount))
}

// InvariantNoOverdraft is the property a plain PBT would check. It is
// weaker than the model: it misses the SelfTransfer mutant.
func (m *machine) InvariantNoOverdraft(tc hegel.TestCase) {
	for a, b := range m.bank.Balances() {
		if b < 0 {
			tc.Errorf("%s overdrawn: %d", a, b)
		}
	}
}

func cfg(mode quinthegel.Mode) quinthegel.Config {
	return quinthegel.Config{Binding: "bank.binding.yaml", Mode: mode, Global: true, AllowEmpty: true}
}

func runMachine(m Mutation) func(*hegel.T, *quinthegel.Recorder) {
	return func(ht *hegel.T, _ *quinthegel.Recorder) {
		hegel.RunStateful(ht, &machine{bank: New(accounts, WithMutation(m))}, hegel.WithStatefulStepCount(12))
	}
}

// Hegel drives the faithful bank; every run must be a behaviour of the
// model, with the balances it reports, and keep noOverdraft. The runs are
// checked after the test, in parallel (After).
func TestHegelRunsConformToModel(t *testing.T) {
	needQuint(t)
	quinthegel.Test(t, cfg(quinthegel.After), runMachine(Faithful), hegel.WithTestCases(40), hegel.WithDatabase(""))
}

// The same machine against the SelfTransfer mutant, with each run checked
// as it finishes (Each): the model rejects the self-transfer, so Hegel
// shrinks to the shortest run that shows it (a deposit, then a transfer
// to the same account). The machine's own invariant never fails.
func TestHegelCatchesSelfTransfer(t *testing.T) {
	needQuint(t)
	if testing.Short() {
		t.Skip("shrinking against the model runs quint ~100 times")
	}
	c, err := quinthegel.Checker("bank.binding.yaml", validate.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	cc := quinthegel.NewCaseChecker(c)
	var last []qtrace.Step
	err = hegel.Run(func(tc hegel.TestCase) {
		rec := quinthegel.NewRecorder(tc)
		defer rec.Install()()
		hegel.RunStateful(tc, &machine{bank: New(accounts, WithMutation(SelfTransfer))}, hegel.WithStatefulStepCount(12))
		last = rec.Steps()
		quinthegel.CheckCase(tc, cc, last)
	}, hegel.WithTestCases(50), hegel.WithDatabase(""), hegel.WithSeed(3))
	q, hits := cc.Stats()
	t.Logf("%d quint checks, %d decided from earlier verdicts; shrunk run:\n%s", q, hits, quinthegel.Render(last))
	if err == nil {
		t.Fatal("the SelfTransfer mutant passed")
	}
	var self bool
	for _, s := range last {
		self = self || (s.Action == "transfer" && s.Outcome == "ok" && s.Args["src"] == s.Args["dst"])
	}
	if !self {
		t.Fatalf("the shrunk run has no successful self-transfer:\n%s", quinthegel.Render(last))
	}
}

// The model drives, Hegel varies: Hegel picks a model trace and a prefix
// of it, and around every model step the driver performs operations the
// model abstracts away: invalid requests (unknown accounts, non-positive
// amounts) that must fail and change nothing. The balances are compared
// with the model after every step.
func TestHegelVariesModelTraces(t *testing.T) {
	needQuint(t)
	traces := quinthegel.Traces(t, connect.Config{Spec: "model/bank.qnt", Main: "bankDesign", MaxSamples: 30, MaxSteps: 20, Seed: "0xb4"})
	hegel.Test(t, func(ht *hegel.T) {
		quinthegel.Replay(ht, traces, func(tc hegel.TestCase) connect.Driver {
			d := &driver{}
			return &noisyDriver{driver: d, tc: tc}
		}, quinthegel.ReplayOptions{Prefix: true})
	}, hegel.WithTestCases(50), hegel.WithDatabase(""))
}

type noisyDriver struct {
	*driver
	tc hegel.TestCase
}

func (n *noisyDriver) Step(s *connect.Step) error {
	if err := n.driver.Step(s); err != nil {
		return err
	}
	ctx := context.Background()
	for k := hegel.Draw(n.tc, hegel.Integers(0, 2)); k > 0; k-- {
		var err error
		switch hegel.Draw(n.tc, hegel.Integers(0, 2)) {
		case 0:
			err = n.bank.Deposit(ctx, "mallory", 1)
		case 1:
			err = n.bank.Withdraw(ctx, "alice", hegel.Draw(n.tc, hegel.Integers(-3, 0)))
		case 2:
			err = n.bank.Transfer(ctx, "bob", "bob", 1)
		}
		if err == nil {
			return errors.New("an invalid request succeeded")
		}
	}
	return nil
}
