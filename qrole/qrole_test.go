package qrole_test

import (
	"context"
	"errors"
	"runtime/pprof"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qrole"
)

// An operation waiting between attempts is in the orbit; one with an
// attempt in flight is not. A done function counts once.
func TestRoles(t *testing.T) {
	r := qrole.New()
	ctx := qrole.WithRoles(context.Background(), r)
	op := qrole.Operation(ctx, "api")
	call := qrole.Call(ctx, "api")
	serve := qrole.Serve(ctx, "api")
	if u, v := r.Queue("api"), r.Orbit("api"); u != 1 || v != 0 {
		t.Fatalf("attempt in flight: u=%d v=%d", u, v)
	}
	serve(errors.New("late"))
	call(errors.New("timeout"))
	call(nil) // counted once
	if u, v := r.Queue("api"), r.Orbit("api"); u != 0 || v != 1 {
		t.Fatalf("waiting to retry: u=%d v=%d", u, v)
	}
	op(nil)
	if ok, failed := r.Attempts("api"); ok != 0 || failed != 1 {
		t.Errorf("attempts %d ok, %d failed", ok, failed)
	}
	if ok, failed := r.Served("api"); ok != 0 || failed != 1 {
		t.Errorf("served %d ok, %d failed", ok, failed)
	}
	if qrole.From(context.Background()) != qrole.Default {
		t.Error("a bare context does not record into Default")
	}
}

// Instrument exports u and v as gauges with the labels qmeta's rules use.
func TestInstrument(t *testing.T) {
	r := qrole.New()
	ctx := qrole.WithRoles(context.Background(), r)
	reader := sdkmetric.NewManualReader()
	if err := r.Instrument(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("test")); err != nil {
		t.Fatal(err)
	}
	qrole.Serve(ctx, "api")
	qrole.Operation(ctx, "api")
	qrole.Operation(ctx, "api")
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &rm); err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if g, ok := m.Data.(metricdata.Gauge[int64]); ok {
				for _, p := range g.DataPoints {
					for _, k := range []attribute.Key{"name", "service"} {
						if v, ok := p.Attributes.Value(k); ok {
							got[m.Name+"/"+v.AsString()] = p.Value
						}
					}
				}
			}
		}
	}
	if got[qrole.MetricServeInflight+"/api"] != 1 || got[qrole.MetricOrbit+"/api"] != 2 {
		t.Errorf("gauges %v", got)
	}
}

// A woven call in a function with no context passes context.TODO(): it
// still counts into its own run's registry (through the goroutine's run),
// not into Default, which runs in parallel would share.
func TestRolesOfTheGoroutinesRun(t *testing.T) {
	r := qrole.New()
	s := qenv.New(qenv.WithContext(qenv.Nominal{}, qrole.WithRoles(context.Background(), r)))
	s.Process("worker", false, func(context.Context) {
		done := qrole.Serve(context.TODO(), "api")
		if q := r.Queue("api"); q != 1 {
			t.Errorf("queue %d in the run's registry", q)
		}
		done(nil)
	})
	if err := s.Run(time.Second); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if ok, _ := r.Served("api"); ok != 1 {
		t.Errorf("served %d", ok)
	}
	if ok, _ := qrole.Default.Served("api"); ok != 0 {
		t.Error("counted into Default")
	}
}

// With Labels on, a goroutine serving a request carries its server's
// label, and loses it when the request is done: a profile splits by role.
func TestLabels(t *testing.T) {
	qrole.Labels(true)
	defer qrole.Labels(false)
	profile := func() string {
		var b strings.Builder
		if err := pprof.Lookup("goroutine").WriteTo(&b, 1); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	ctx := qrole.WithRoles(context.Background(), qrole.New())
	done := qrole.Serve(ctx, "api")
	if p := profile(); !strings.Contains(p, `"quint.serve":"api"`) {
		t.Fatalf("no label while serving:\n%.2000s", p)
	}
	done(nil)
	if p := profile(); strings.Contains(p, `"quint.serve":"api"`) {
		t.Error("the label outlives the request")
	}
}
