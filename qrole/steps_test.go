package qrole_test

import (
	"context"
	"errors"
	"testing"

	"github.com/casselc/quint-connect-go/qobs"
	"github.com/casselc/quint-connect-go/qrole"
	"github.com/casselc/quint-connect-go/qtrace"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

type countSink struct{ n int }

func (c *countSink) Record(context.Context, *qtrace.Step) { c.n++ }

// Steps are counted by action and outcome and passed on.
func TestMetricsSink(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	next := &countSink{}
	sink, err := qrole.NewMetricsSink(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("test"), next)
	if err != nil {
		t.Fatal(err)
	}
	r := qobs.NewRecorder(sink)
	ctx := context.Background()
	r.Record(ctx, "deposit", nil, "amount", 5)
	r.Record(ctx, "deposit", nil, "amount", 6)
	r.Record(ctx, "deposit", errors.New("no"), "amount", 7)
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &rm); err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			sum, ok := m.Data.(metricdata.Sum[int64])
			if m.Name != qrole.MetricSteps || !ok {
				continue
			}
			for _, p := range sum.DataPoints {
				a, _ := p.Attributes.Value(qtrace.KeyAction)
				o, _ := p.Attributes.Value(qtrace.KeyOutcome)
				got[a.AsString()+"/"+o.AsString()] = p.Value
			}
		}
	}
	if got["deposit/ok"] != 2 || got["deposit/error"] != 1 || next.n != 3 {
		t.Errorf("counts %v, passed on %d", got, next.n)
	}
}
