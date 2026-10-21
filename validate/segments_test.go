package validate

import (
	"context"
	"os/exec"
	"testing"

	"github.com/casselc/quint-connect-go/qobs"
	"github.com/casselc/quint-connect-go/qtrace"
)

// Long runs are replayed in segments; a disabled step is located wherever
// it falls: inside a segment, at a segment's end, or last of all.
func TestDisabledStepLocatedInLongRuns(t *testing.T) {
	needQuint(t)
	c, err := NewChecker("testdata/account.yaml", Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	run := func(n, bad int) []qtrace.Step {
		sink := &qobs.MemorySink{}
		r := qobs.NewRecorder(sink, qobs.WithActor("acct"))
		bal := 0
		for i := 0; i < n; i++ {
			if i == bad {
				r.Record(context.Background(), "withdraw", nil, "amount", 1000000, "obs.balance", 0)
				continue
			}
			bal++
			r.Record(context.Background(), "deposit", nil, "amount", 1, "obs.balance", bal)
		}
		return sink.Steps()
	}
	for _, tc := range []struct{ n, bad int }{{70, 33}, {70, 19}, {70, 69}, {10, 9}, {10, 4}, {500, 433}, {500, 399}} {
		rep, err := c.Check(context.Background(), run(tc.n, tc.bad))
		if err != nil {
			t.Fatal(err)
		}
		f := rep.Actors[0].Failure
		if rep.OK() || f == nil || f.Kind != "disabled" || f.Index != tc.bad {
			t.Errorf("%d steps, bad step %d: got\n%s", tc.n, tc.bad, rep.Summary())
		}
	}
	rep, err := c.Check(context.Background(), run(90, -1)) // within the account model's LIMIT of 100
	if err != nil || !rep.OK() {
		t.Fatalf("a long conforming run: %v\n%s", err, rep.Summary())
	}
}

func needQuint(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("quint"); err != nil {
		t.Skip("quint not installed")
	}
}
