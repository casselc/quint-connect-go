package validate

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/casselc/quint-connect-go/qobs"
	"github.com/casselc/quint-connect-go/qtrace"
)

// CheckBatch gives the same verdicts as Check, at the same steps, with far
// fewer quint processes.
func TestCheckBatchMatchesCheck(t *testing.T) {
	needQuint(t)
	c, err := NewChecker("testdata/account.yaml", Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	rec := func(ops ...any) []qtrace.Step {
		sink := &qobs.MemorySink{}
		r := qobs.NewRecorder(sink, qobs.WithActor("acct"))
		for i := 0; i+2 < len(ops); i += 3 {
			r.Record(context.Background(), ops[i].(string), nil, "amount", ops[i+1], "obs.balance", ops[i+2])
		}
		return sink.Steps()
	}
	var runs [][]qtrace.Step
	for i := 0; i < 24; i++ {
		switch i % 4 {
		case 0:
			runs = append(runs, rec("deposit", 5, 5, "withdraw", 2, 3, "deposit", i+1, 4+i))
		case 1:
			runs = append(runs, rec("deposit", 5, 5, "withdraw", 9, -4)) // disabled
		case 2:
			runs = append(runs, rec("deposit", 5, 5, "deposit", 5, 11)) // state
		case 3:
			runs = append(runs, rec("deposit", 60, 60, "deposit", 50, 110)) // invariant underLimit
		}
	}
	verdict := func(r *Report) string {
		a := r.Actors[0]
		s := fmt.Sprint(a.Conforms)
		if a.Failure != nil {
			s += fmt.Sprintf(" %s@%d", a.Failure.Kind, a.Failure.Index)
		}
		for _, inv := range a.Invariants {
			s += " " + inv.Name + "=" + string(inv.Verdict)
			if inv.Failure != nil {
				s += fmt.Sprintf("@%d", inv.Failure.Index)
			}
		}
		return s
	}
	t0 := time.Now()
	batch, errs := c.CheckBatch(context.Background(), runs, 0)
	tBatch := time.Since(t0)
	t0 = time.Now()
	par, perrs := c.CheckRuns(context.Background(), runs, 0)
	tPar := time.Since(t0)
	for i := range runs {
		if errs[i] != nil || perrs[i] != nil {
			t.Fatalf("run %d: %v / %v", i, errs[i], perrs[i])
		}
		if verdict(batch[i]) != verdict(par[i]) {
			t.Errorf("run %d: batch %q, one by one %q", i, verdict(batch[i]), verdict(par[i]))
		}
	}
	t0 = time.Now()
	for _, r := range runs[:6] {
		if _, err := c.Check(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
	tSeq := time.Since(t0) * time.Duration(len(runs)) / 6
	t.Logf("%d runs: CheckBatch %v, CheckRuns (parallel) %v, Check one at a time ~%v", len(runs), tBatch.Round(time.Millisecond),
		tPar.Round(time.Millisecond), tSeq.Round(time.Millisecond))
}
