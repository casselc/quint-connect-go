package choice

import (
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
