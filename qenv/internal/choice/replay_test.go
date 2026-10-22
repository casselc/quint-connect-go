package choice

import (
	"fmt"
	"strings"
	"testing"
)

// A replay that diverges stops there and says where, instead of panicking
// in the task that asked: the exploration reports it as the run's error.
func TestReplayDivergence(t *testing.T) {
	r := Replay([]Choice{{Kind: "sched.task", N: 2, Value: 1}, {Kind: "fault.x", N: 2, Value: 1, Step: 3}})
	if r.Choose("sched.task", 2) != 1 || r.Err() != nil {
		t.Fatal("the recorded choice")
	}
	if r.Choose("fault.y", 2) != 0 || r.Err() == nil || !strings.Contains(r.Err().Error(), "at choice 1 (after 3 steps): recorded fault.x/2, asked fault.y/2") {
		t.Fatalf("divergence: %v", r.Err())
	}
	if r.Choose("fault.x", 2) != 0 {
		t.Error("replayed after diverging")
	}
}

// An encoded run carries the byte rule's version: its own is skipped,
// another's is an error, and raw bytes (a fuzzer's) are choices as they are.
func TestEncodedRunsAreVersioned(t *testing.T) {
	rates := Rates{Default: 0.5}
	choices := []Choice{{Kind: "sched.x", N: 3, Value: 2}, {Kind: "fault.y", N: 2, Value: 1}}
	data, err := EncodeChoices(choices, rates)
	if err != nil {
		t.Fatal(err)
	}
	b := FromBytes(data, rates)
	if b.Choose("sched.x", 3) != 2 || b.Choose("fault.y", 2) != 1 || b.Err() != nil {
		t.Fatalf("own version: %v", b.Err())
	}
	data[3] = BytesVersion + 1
	if b := FromBytes(data, rates); b.Choose("sched.x", 3) != 0 || b.Err() == nil {
		t.Fatalf("another version replayed: %v", b.Err())
	}
	if b := FromBytes([]byte{2}, rates); b.Choose("sched.x", 3) != 2 {
		t.Fatal("raw bytes")
	}
}

// A rate's kind covers itself and the kinds below it, not every kind its
// name prefixes: "log" once set the rate of "login".
func TestRatesCoverKindsBelow(t *testing.T) {
	r := Rates{Default: 0.1, Kinds: map[string]float64{"log": 0.5, "log.append": 0.9}}
	for kind, want := range map[string]float64{"log": 0.5, "log.read": 0.5, "log.append": 0.9, "login": 0.1} {
		if got := r.Of(kind); got != want {
			t.Errorf("Of(%s) = %v, want %v", kind, got, want)
		}
	}
}

// Users name kinds as reports print them; a key with the chooser's
// "fault." prefix would match nothing, so it fails with the fix.
func TestRatesWantBareKinds(t *testing.T) {
	if r := (Rates{Kinds: map[string]float64{"nemesis": 0.5}}); r.Of("fault.nemesis") != 0.5 || r.Of("nemesis") != 0.5 {
		t.Errorf("a bare key's rate: %v %v", r.Of("fault.nemesis"), r.Of("nemesis"))
	}
	if NoFaults.Of("fault.nemesis") != 0 || NoFaults.Of("fault.store.get") != 0 {
		t.Error("NoFaults injects a fault")
	}
	defer func() {
		if msg := fmt.Sprint(recover()); !strings.Contains(msg, `name the kind without the "fault." prefix ("rpc")`) {
			t.Errorf("panic %q, want the bare kind", msg)
		}
	}()
	Rates{Kinds: map[string]float64{"fault.rpc": 0.5}}.Of("fault.rpc.x")
	t.Error("a prefixed key was accepted")
}

// A fault prints as reports name it, by its option's name when it has one.
func TestChoiceString(t *testing.T) {
	for _, c := range []struct {
		c    Choice
		want string
	}{
		{Choice{Kind: "fault.rpc.counter.request", N: 3, Value: 2, Name: "duplicate"}, "rpc.counter.request=duplicate"},
		{Choice{Kind: "fault.x", N: 2, Value: 1}, "x=1"},
		{Choice{Kind: "sched.task", N: 3, Value: 1}, "sched.task=1/3"},
	} {
		if got := c.c.String(); got != c.want {
			t.Errorf("%#v: %q, want %q", c.c, got, c.want)
		}
	}
}
