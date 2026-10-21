package quinthegel_test

import (
	"errors"
	"os/exec"
	"testing"

	"github.com/casselc/quint-connect-go/connect"
	"github.com/casselc/quint-connect-go/qtrace"
	"github.com/casselc/quint-connect-go/quinthegel"
	"github.com/casselc/quint-connect-go/validate"
	"hegel.dev/go/hegel"
)

// bank is the system under test. With buggy set, a transfer to oneself
// double-credits (the model forbids src == dst; the bug is that the
// implementation accepts it).
type bank struct {
	bal   map[string]int
	buggy bool
}

func (b *bank) deposit(a string, n int) { b.bal[a] += n }
func (b *bank) withdraw(a string, n int) error {
	if b.bal[a] < n {
		return errors.New("insufficient funds")
	}
	b.bal[a] -= n
	return nil
}
func (b *bank) transfer(src, dst string, n int) error {
	if src == dst && !b.buggy {
		return errors.New("same account")
	}
	if err := b.withdraw(src, n); err != nil {
		return err
	}
	b.bal[dst] += n
	return nil
}

var accounts = []string{"alice", "bob"}

// bankMachine is a Hegel state machine over the bank; every rule records
// the model step it performed, with the balances it observed afterwards.
type bankMachine struct {
	b   *bank
	rec *quinthegel.Recorder
}

func (m *bankMachine) obs() []any {
	return []any{"obs.alice", m.b.bal["alice"], "obs.bob", m.b.bal["bob"]}
}

func (m *bankMachine) RuleDeposit(tc hegel.TestCase) {
	a, n := hegel.Draw(tc, hegel.SampledFrom(accounts)), hegel.Draw(tc, hegel.Integers(1, 5))
	m.b.deposit(a, n)
	m.rec.Step("deposit", nil, append([]any{"account", a, "amount", n}, m.obs()...)...)
}

func (m *bankMachine) RuleWithdraw(tc hegel.TestCase) {
	a, n := hegel.Draw(tc, hegel.SampledFrom(accounts)), hegel.Draw(tc, hegel.Integers(1, 5))
	err := m.b.withdraw(a, n)
	m.rec.Step("withdraw", err, append([]any{"account", a, "amount", n}, m.obs()...)...)
}

func (m *bankMachine) RuleTransfer(tc hegel.TestCase) {
	src, dst := hegel.Draw(tc, hegel.SampledFrom(accounts)), hegel.Draw(tc, hegel.SampledFrom(accounts))
	n := hegel.Draw(tc, hegel.Integers(1, 5))
	err := m.b.transfer(src, dst, n)
	m.rec.Step("transfer", err, append([]any{"src", src, "dst", dst, "amount", n}, m.obs()...)...)
}

func (m *bankMachine) InvariantNonNegative(tc hegel.TestCase) {
	for a, v := range m.b.bal {
		if v < 0 {
			tc.Errorf("%s is overdrawn: %d", a, v)
		}
	}
}

func needQuint(t *testing.T) {
	if _, err := exec.LookPath("quint"); err != nil {
		t.Skip("quint not installed")
	}
}

func run(buggy bool) func(ht *hegel.T, rec *quinthegel.Recorder) {
	return func(ht *hegel.T, rec *quinthegel.Recorder) {
		m := &bankMachine{b: &bank{bal: map[string]int{"alice": 0, "bob": 0}, buggy: buggy}, rec: rec}
		hegel.RunStateful(ht, m, hegel.WithStatefulStepCount(8))
	}
}

func TestStateMachineConformsEach(t *testing.T) {
	needQuint(t)
	quinthegel.Test(t, quinthegel.Config{Binding: "testdata/bank.yaml", Mode: quinthegel.Each}, run(false),
		hegel.WithTestCases(5), hegel.WithDatabase(""))
}

func TestStateMachineConformsAfter(t *testing.T) {
	needQuint(t)
	quinthegel.Test(t, quinthegel.Config{Binding: "testdata/bank.yaml", Mode: quinthegel.After}, run(false),
		hegel.WithTestCases(20), hegel.WithDatabase(""))
}

// The bug is invisible to the machine's own invariant (no balance goes
// negative) but the model rejects the self-transfer: Hegel shrinks the run
// to the one step the model cannot take.
func TestStateMachineBugShrinks(t *testing.T) {
	needQuint(t)
	if testing.Short() {
		t.Skip("shrinking against the model runs quint ~130 times (~1.5 min)")
	}
	c, err := quinthegel.Checker("testdata/bank.yaml", validate.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	cc := quinthegel.NewCaseChecker(c)
	var last []qtrace.Step
	err = hegel.Run(func(tc hegel.TestCase) {
		rec := quinthegel.NewRecorder(tc)
		m := &bankMachine{b: &bank{bal: map[string]int{"alice": 0, "bob": 0}, buggy: true}, rec: rec}
		hegel.RunStateful(tc, m, hegel.WithStatefulStepCount(8))
		last = rec.Steps()
		quinthegel.CheckCase(tc, cc, last)
	}, hegel.SuppressHealthCheck(hegel.TooSlow), hegel.WithTestCases(40), hegel.WithDatabase(""), hegel.WithSeed(7))
	q, hits := cc.Stats()
	t.Logf("%d quint runs, %d cache hits; shrunk run:\n%s", q, hits, quinthegel.Render(last))
	if err == nil {
		t.Fatal("the buggy bank passed")
	}
	if len(last) == 0 || last[len(last)-1].Action != "transfer" || len(last) > 2 {
		t.Fatalf("not shrunk to a self-transfer:\n%s", quinthegel.Render(last))
	}
}

// Model-driven: Quint's traces drive the bank inside Hegel, which picks the
// trace and the prefix.
func TestReplayTraces(t *testing.T) {
	needQuint(t)
	traces := quinthegel.Traces(t, connect.Config{Spec: "../connect/testdata/bank.qnt", MaxSamples: 20, MaxSteps: 10, Seed: "0x2a"})
	hegel.Test(t, func(ht *hegel.T) {
		quinthegel.Replay(ht, traces, func(tc hegel.TestCase) connect.Driver {
			b := &bank{bal: map[string]int{"alice": 0, "bob": 0}}
			return connect.Switch{
				"init": connect.Skip,
				"deposit": func(s *connect.Step) error {
					b.deposit(connect.MustPick[string](s, "account"), connect.MustPick[int](s, "amount"))
					return nil
				},
				"withdraw": func(s *connect.Step) error {
					return b.withdraw(connect.MustPick[string](s, "account"), connect.MustPick[int](s, "amount"))
				},
				"transfer": func(s *connect.Step) error {
					return b.transfer(connect.MustPick[string](s, "src"), connect.MustPick[string](s, "dst"), connect.MustPick[int](s, "amount"))
				},
			}
		}, quinthegel.ReplayOptions{Prefix: true})
	}, hegel.WithTestCases(30), hegel.WithDatabase(""))
}
