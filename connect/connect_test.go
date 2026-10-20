package connect

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/casselc/quint-connect-go/itf"
)

// bank is the implementation under test.
type bank struct {
	balances map[string]int
	ops      int
	buggy    bool // transfers credit the destination twice
}

func (b *bank) deposit(a string, n int) { b.balances[a] += n; b.ops++ }
func (b *bank) withdraw(a string, n int) error {
	if b.balances[a] < n {
		return errors.New("insufficient funds")
	}
	b.balances[a] -= n
	b.ops++
	return nil
}
func (b *bank) transfer(src, dst string, n int) error {
	if err := b.withdraw(src, n); err != nil {
		return err
	}
	b.balances[dst] += n
	if b.buggy && n == 3 {
		b.balances[dst]++
	}
	return nil
}

// bankState is the part of the model's state the driver compares.
type bankState struct {
	Balances map[string]int
	Ops      int
}

type bankDriver struct {
	b     *bank
	buggy bool
}

func (d *bankDriver) Step(s *Step) error {
	return Switch{
		"init": func(*Step) error {
			d.b = &bank{balances: map[string]int{"alice": 0, "bob": 0}, buggy: d.buggy}
			return nil
		},
		"deposit": func(s *Step) error {
			d.b.deposit(MustPick[string](s, "account"), MustPick[int](s, "amount"))
			return nil
		},
		"withdraw": func(s *Step) error {
			return d.b.withdraw(MustPick[string](s, "account"), MustPick[int](s, "amount"))
		},
		"transfer": func(s *Step) error {
			return d.b.transfer(MustPick[string](s, "src"), MustPick[string](s, "dst"), MustPick[int](s, "amount"))
		},
	}.Step(s)
}

func (d *bankDriver) State() (any, error) {
	return bankState{Balances: d.b.balances, Ops: d.b.ops}, nil
}

func needQuint(t *testing.T) {
	if _, err := exec.LookPath("quint"); err != nil {
		t.Skip("quint not installed")
	}
}

func TestRunBank(t *testing.T) {
	needQuint(t)
	Run(t, Config{Spec: "testdata/bank.qnt", MaxSamples: 10, MaxSteps: 15}, func() Driver { return &bankDriver{} })
}

// fakeT records Fatalf instead of stopping the test.
type fakeT struct {
	testing.TB
	failed string
}

func (f *fakeT) Fatalf(format string, args ...any) {
	f.failed = strings.TrimSpace(strings.ReplaceAll(sprintf(format, args...), "\n", " | "))
	panic(f)
}
func (f *fakeT) Helper() {}

func catch(t *testing.T, f func(ft *fakeT)) string {
	ft := &fakeT{TB: t}
	func() {
		defer func() {
			if r := recover(); r != nil && r != ft {
				panic(r)
			}
		}()
		f(ft)
	}()
	return ft.failed
}

func TestRunFindsBug(t *testing.T) {
	needQuint(t)
	msg := catch(t, func(ft *fakeT) {
		Run(ft, Config{Spec: "testdata/bank.qnt", MaxSamples: 50, MaxSteps: 20, Seed: "0x1"}, func() Driver { return &bankDriver{buggy: true} })
	})
	t.Log(msg)
	for _, want := range []string{"states diverge", ".Balances[", "transfer(", "QUINT_SEED=0x1"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("missing %q in failure:\n%s", want, msg)
		}
	}
}

func TestUnimplementedAction(t *testing.T) {
	needQuint(t)
	msg := catch(t, func(ft *fakeT) {
		Run(ft, Config{Spec: "testdata/bank.qnt", MaxSamples: 5, MaxSteps: 10}, func() Driver {
			d := &bankDriver{}
			return DriverFunc(func(s *Step) error {
				if s.Action == "transfer" {
					return Switch{}.Step(s)
				}
				return d.Step(s)
			})
		})
	})
	t.Log(msg)
	if !strings.Contains(msg, `unimplemented action "transfer"`) {
		t.Fatalf("failure: %s", msg)
	}
}

func TestQuintTestNeedsNondetPath(t *testing.T) {
	needQuint(t)
	msg := catch(t, func(ft *fakeT) {
		Test(ft, Config{Spec: "testdata/bank.qnt", MaxSamples: 1}, "aliceSavesTest", func() Driver { return &bankDriver{} })
	})
	if !strings.Contains(msg, "no mbt::actionTaken") {
		t.Fatalf("failure: %s", msg)
	}
}

func TestNondetPath(t *testing.T) {
	// A Choreo-style spec records the action as a sum type in its state.
	src := `{"vars":["s"],"states":[
	  {"s":{"last":{"tag":"Init","value":{"#tup":[]}},"n":{"#bigint":"0"}}},
	  {"s":{"last":{"tag":"Add","value":{"k":{"#bigint":"2"},"opt":{"tag":"None","value":{"#tup":[]}}}},"n":{"#bigint":"2"}}}]}`
	tr, err := itf.Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	var seen []string
	d := DriverFunc(func(s *Step) error {
		seen = append(seen, s.Action)
		if s.Action == "Add" {
			k, err := Pick[int](s, "k")
			if err != nil {
				return err
			}
			if p, _ := OptPick[int](s, "opt"); p != nil {
				return errors.New("None must be absent")
			}
			n += k
		}
		return nil
	})
	if err := Replay(tr, d, ReplayOptions{NondetPath: []string{"s", "last"}, StatePath: []string{"s"}}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(seen, ",") != "Init,Add" || n != 2 {
		t.Fatalf("seen %v n %d", seen, n)
	}
}

func TestDiff(t *testing.T) {
	d := Diff(bankState{Balances: map[string]int{"a": 1, "b": 2}, Ops: 3}, bankState{Balances: map[string]int{"a": 1, "b": 5}, Ops: 3})
	if len(d) != 1 || d[0] != ".Balances[b]: model 2, implementation 5" {
		t.Fatalf("%q", d)
	}
}

func sprintf(format string, args ...any) string { return fmt.Sprintf(format, args...) }
