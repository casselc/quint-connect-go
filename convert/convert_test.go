package convert

import (
	"context"
	"testing"

	"github.com/casselc/quint-connect-go/otelio"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

const rules = `
actor: resource:service.name
rules:
  - span: "Bank/Health*"
    drop: true
  - span: "Bank/Deposit"
    action: deposit
    args: { account: attr:bank.account, amount: attr:bank.amount|int }
    obs: { balance: attr:bank.balance }
  - span: "Bank/Withdraw"
    action: withdraw
    args: { account: attr:bank.account, amount: attr:bank.amount }
  - span: "Bank/Audit"
    event: audited
    action: audit
    args: { n: event:count }
`

// An application's own spans, with no quint instrumentation, become steps.
func TestConvertAppSpans(t *testing.T) {
	r, err := Parse([]byte(rules))
	if err != nil {
		t.Fatal(err)
	}
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr),
		sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", "bank"), attribute.String("service.instance.id", "i-1"))))
	tr := tp.Tracer("app")
	ctx := context.Background()
	_, s := tr.Start(ctx, "Bank/Deposit")
	s.SetAttributes(attribute.String("bank.account", "alice"), attribute.String("bank.amount", "5"), attribute.Int64("bank.balance", 5))
	s.End()
	_, s = tr.Start(ctx, "Bank/HealthCheck")
	s.End()
	_, s = tr.Start(ctx, "Bank/Withdraw")
	s.SetAttributes(attribute.String("bank.account", "alice"), attribute.Int64("bank.amount", 9))
	s.SetStatus(codes.Error, "insufficient funds")
	s.End()
	_, s = tr.Start(ctx, "Bank/Audit")
	s.AddEvent("audited", traceAttrs(attribute.Int64("count", 2)))
	s.AddEvent("audited", traceAttrs(attribute.Int64("count", 3)))
	s.End()
	_, s = tr.Start(ctx, "Other")
	s.End()
	td := otelio.SpansToTracesData(sr.Ended())
	steps, err := r.Steps([]otelio.TracesData{td})
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 4 {
		t.Fatalf("%d steps: %+v", len(steps), steps)
	}
	d, w := steps[0], steps[1]
	if d.Action != "deposit" || d.Args["amount"] != int64(5) || d.Obs["balance"] != int64(5) || d.Actor != "bank" || d.Seq != 1 || d.Recorder != "i-1" || d.Outcome != "ok" {
		t.Fatalf("deposit: %+v", d)
	}
	if w.Action != "withdraw" || w.Outcome != "error" || w.Error != "insufficient funds" || w.Seq != 2 {
		t.Fatalf("withdraw: %+v", w)
	}
	if steps[2].Action != "audit" || steps[2].Args["n"] != int64(2) || steps[3].Args["n"] != int64(3) {
		t.Fatalf("events: %+v %+v", steps[2], steps[3])
	}
}

func traceAttrs(kv ...attribute.KeyValue) trace.EventOption { return trace.WithAttributes(kv...) }
