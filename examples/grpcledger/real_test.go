package grpcledger_test

import (
	"context"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/casselc/quint-connect-go/convert"
	"github.com/casselc/quint-connect-go/examples/grpcledger"
	"github.com/casselc/quint-connect-go/examples/grpcledger/ledger"
	"github.com/casselc/quint-connect-go/otelio"
	"github.com/casselc/quint-connect-go/qtest"
	"github.com/casselc/quint-connect-go/qtrace"
)

// realSteps serves the ledger for real, runs a concurrent load with the
// interceptor's faults, and converts the server's otelgrpc spans to steps
// with ledger.convert.yaml: how a production gRPC service is checked, from
// the spans it already exports.
func realSteps(t *testing.T, cfg grpcledger.RealConfig) []qtrace.Step {
	t.Helper()
	sr := tracetest.NewSpanRecorder()
	cfg.Tracer = sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	cl, stop, err := grpcledger.Serve(cfg)
	if err != nil {
		t.Fatal(err)
	}
	grpcledger.Load(context.Background(), cl, 4, 8, cfg.Seed)
	stop()
	rules, err := convert.Load("ledger.convert.yaml")
	if err != nil {
		t.Fatal(err)
	}
	steps, err := rules.Steps([]otelio.TracesData{otelio.SpansToTracesData(sr.Ended())})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d spans, %d steps", len(sr.Ended()), len(steps))
	return steps
}

// The real server's spans conform through refused connections and lost
// responses: every retry of a transfer is a replay.
func TestRealSpansConform(t *testing.T) {
	qtest.RequireQuint(t)
	steps := realSteps(t, grpcledger.RealConfig{Seed: 1, Refuse: 0.1, LoseResponse: 0.2})
	qtest.Expect(t, qtest.Validate(t, binding, steps).Report, qtest.Conforms())
}

// The bugs the simulation finds, from a real server's spans: the missing
// dedupe once responses are lost, the overdraft from the load alone.
// The real server's interleaving varies with the machine's load, so the
// same bug shows as a step the model cannot take or, when the commits
// interleave otherwise, as a balance the model does not have.
func TestRealMutants(t *testing.T) {
	qtest.RequireQuint(t)
	for _, c := range []struct {
		m    ledger.Mutation
		lose float64
		want qtest.Expectation
	}{{ledger.NoDedupe, 0.3, qtest.AnyOf(reapplied, qtest.Mismatch(""))},
		{ledger.NoFundsCheck, 0, qtest.AnyOf(qtest.Rejects("transfer"), qtest.Mismatch("transfer"))}} {
		t.Run(c.m.String(), func(t *testing.T) {
			steps := realSteps(t, grpcledger.RealConfig{Mutation: c.m, Seed: 1, LoseResponse: c.lose})
			qtest.Expect(t, qtest.Validate(t, binding, steps).Report, c.want)
		})
	}
}
