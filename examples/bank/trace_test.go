package bank

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/casselc/quint-connect-go/convert"
	"github.com/casselc/quint-connect-go/otelio"
	"github.com/casselc/quint-connect-go/qobs"
	"github.com/casselc/quint-connect-go/qtest"
	"github.com/casselc/quint-connect-go/validate"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// payday is a scenario a service might run in production: concurrent
// deposits, then transfers, some of which are refused.
func payday(b *Bank) {
	ctx := context.Background()
	done := make(chan struct{})
	for _, a := range accounts {
		go func(a string) {
			defer func() { done <- struct{}{} }()
			for i := 0; i < 3; i++ {
				_ = b.Deposit(ctx, a, 2)
			}
		}(a)
	}
	for range accounts {
		<-done
	}
	_ = b.Transfer(ctx, "alice", "bob", 4)
	_ = b.Transfer(ctx, "carol", "carol", 1) // refused
	_ = b.Withdraw(ctx, "bob", 99)           // refused
	_ = b.Withdraw(ctx, "bob", 9)
}

// The bank reports its model steps (bank.go's qobs.Record calls); qtest
// records a run and checks it: every step a transition of the model, the
// reported balances the model's, noOverdraft in every state. The step log
// and a model-level ITF of the run are left in the test's directory
// ($QUINTGO_KEEP keeps them).
func TestRecordedRunConforms(t *testing.T) {
	qtest.RequireQuint(t)
	qtest.RecordAndCheck(t, "bank.binding.yaml", func(*qobs.Recorder) { payday(New(accounts)) })
}

// The same scenario on the LostDeposit mutant: the third deposit to each
// account reports a balance the model does not have.
func TestRecordedRunCatchesLostDeposit(t *testing.T) {
	qtest.RequireQuint(t)
	run := qtest.Record(t, "bank.binding.yaml", func(*qobs.Recorder) { payday(New(accounts, WithMutation(LostDeposit))) })
	run.Expect(t, qtest.Mismatch("deposit"))
	t.Logf("as expected:\n%s", run.Report.Summary())
}

// Telemetry the bank emits anyway, OTel spans with no quint-specific
// attributes, converted to model steps by bank.convert.yaml and checked.
// The OTLP/JSON file is what a collector's file exporter would write; the
// CLI checks it the same way:
//
//	quintgo validate -binding bank.binding.yaml -convert bank.convert.yaml spans.jsonl
func TestOTelSpansConvertedAndChecked(t *testing.T) {
	qtest.RequireQuint(t)
	rules, err := convert.Load("bank.convert.yaml")
	if err != nil {
		t.Fatal(err)
	}
	checker, err := validate.NewChecker("bank.binding.yaml", validate.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []Mutation{Faithful, LostDeposit} {
		sr := tracetest.NewSpanRecorder()
		tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
		payday(New(accounts, WithTracer(tp), WithMutation(m)))

		// Through a file, as from a collector.
		path := filepath.Join(t.TempDir(), "spans.jsonl")
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := otelio.NewFileExporter(f).ExportSpans(context.Background(), sr.Ended()); err != nil {
			t.Fatal(err)
		}
		f.Close()
		f, _ = os.Open(path)
		tds, err := otelio.ReadTracesData(f)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		steps, err := rules.Steps(tds)
		if err != nil {
			t.Fatal(err)
		}
		rep, err := checker.Check(context.Background(), steps)
		if err != nil {
			t.Fatal(err)
		}
		switch {
		case m == Faithful && !rep.OK():
			t.Errorf("faithful bank's spans do not conform:\n%s", rep.Summary())
		case m == LostDeposit && (rep.OK() || !strings.Contains(rep.Summary(), "does not match what was observed")):
			t.Errorf("LostDeposit's spans were not rejected:\n%s", rep.Summary())
		}
	}
}
