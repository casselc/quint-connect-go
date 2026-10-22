package qtest

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/casselc/quint-connect-go/itf"
	"github.com/casselc/quint-connect-go/qobs"
	"github.com/casselc/quint-connect-go/validate"
)

const account = "../validate/testdata/account.yaml"

func opts() []Option {
	return []Option{WithOptions(validate.Options{Backend: "typescript"}), RecorderOptions(qobs.WithActor("acct-1"))}
}

func needQuint(t *testing.T) {
	if _, err := exec.LookPath("quint"); err != nil {
		t.Skip("quint not installed")
	}
}

// A deterministic simulation that emits its own trace: no OTel anywhere.
func TestRecordAndCheck(t *testing.T) {
	needQuint(t)
	ctx := context.Background()
	r := RecordAndCheck(t, account, func(rec *qobs.Recorder) {
		bal := 0
		for _, op := range []struct {
			name string
			n    int
		}{{"deposit", 50}, {"withdraw", 20}, {"deposit", 5}} {
			if op.name == "deposit" {
				bal += op.n
			} else {
				bal -= op.n
			}
			rec.Record(ctx, op.name, nil, "amount", op.n, "obs.balance", bal)
		}
	}, opts()...)
	// The run left a step log and a model-level ITF, and each validates on its own.
	if _, err := os.Stat(r.StepLog); err != nil {
		t.Fatal(err)
	}
	tr, err := itf.ReadFile(r.ITF["acct-1"])
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.States) != 4 || tr.States[2].Action != "withdraw" || tr.States[2].Picks["amount"].Int64() != 20 {
		t.Fatalf("ITF: %+v", tr.States)
	}
	res, err := validate.ValidateITF(ctx, tr, validate.ITFOptions{Options: validate.Options{Backend: "typescript", Dir: t.TempDir()}})
	if err != nil || !res.OK() {
		t.Fatalf("ITF does not validate: %v\n%s", err, res.Summary())
	}
}

func TestRecordReportsTheBrokenStep(t *testing.T) {
	needQuint(t)
	ctx := context.Background()
	r := Record(t, account, func(rec *qobs.Recorder) {
		rec.Record(ctx, "deposit", nil, "amount", 10, "obs.balance", 10)
		rec.Record(ctx, "withdraw", nil, "amount", 30, "obs.balance", -20) // overdraws
	}, opts()...)
	if r.OK() {
		t.Fatal("an overdraft must not conform")
	}
	f := r.Report.Actors[0].Failure
	if f == nil || f.Kind != "disabled" || f.Index != 1 || f.Step.Action != "withdraw" {
		t.Fatalf("failure: %+v", f)
	}
}

// A step from a goroutine the scenario starts has no run to land in
// without Global: Record fails, naming the fix, instead of checking a run
// that lost it. With Global it lands in the run.
func TestRecordGlobalOptIn(t *testing.T) {
	needQuint(t)
	scenario := func(rec *qobs.Recorder) {
		done := make(chan struct{})
		go func() { //qenv:ok a goroutine no run follows: what Global is for
			defer close(done)
			qobs.Record(context.Background(), "deposit", nil, "amount", 10, "obs.balance", 10)
		}()
		<-done
	}
	f := &failTB{TB: t}
	done := make(chan struct{})
	go func() { //qenv:ok failTB's Fatalf ends the goroutine (runtime.Goexit)
		defer close(done)
		Record(f, account, scenario, opts()...)
	}()
	<-done
	if len(f.errs) == 0 || !strings.Contains(f.errs[0], "pass qtest.Global()") {
		t.Errorf("without Global: errors %q, want the escaped step named with the fix", f.errs)
	}
	Record(t, account, scenario, append(opts(), Global())...).Expect(t, Conforms())
	if qobs.Global() != nil {
		t.Error("Record left its global recorder installed")
	}
}
