package qload_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qload"
	"github.com/casselc/quint-connect-go/qobs"
)

// echo is a minimal application: a service of limited capacity, and a
// customer request that calls it and records a model step on success.
func echo(env *qload.Env) func(ctx context.Context, i int) {
	rnd := env.Rand("service")
	svc := qenv.NewService(env.Sim, "echo", nil, func(ctx context.Context, n int) (int, error) { return n, nil })
	svc.Capacity = &qenv.Capacity{Workers: 1, Queue: 20, Slowdown: env.Trigger.Factor(),
		Time: func() time.Duration { return time.Duration(rnd.ExpFloat64() / 10 * float64(time.Second)) }}
	env.Probe("queue", svc.InSystem)
	return func(ctx context.Context, i int) {
		if _, err := svc.Call(ctx, i, time.Second); err == nil {
			qobs.Record(ctx, "served", nil, "n", i)
		}
	}
}

// Goodput is counted from the app's own steps; a run is a function of its
// seed; a closed loop's throughput is bounded by its population.
func TestExperiment(t *testing.T) {
	x := qload.Experiment{Deploy: echo, Rate: 5}
	a := x.Run(1, qload.Trigger{}, 200*time.Second)
	if r := a.Rate("served", 0, 200*time.Second); r < 4.5 || r > 5.5 {
		t.Errorf("open loop at 5/s: goodput %.2f/s", r)
	}
	if b := x.Run(1, qload.Trigger{}, 200*time.Second); !reflect.DeepEqual(a, b) {
		t.Error("two runs with the same seed differ")
	}
	slow := x.Run(1, qload.Trigger{Start: 50 * time.Second, Duration: 50 * time.Second, Slowdown: 4}, 200*time.Second)
	if r := slow.Rate("served", 60*time.Second, 100*time.Second); r > 3 {
		t.Errorf("4x slower (2.5/s capacity): goodput %.2f/s during the trigger", r)
	}
	closed := qload.Experiment{Deploy: echo, Population: 4, Think: 2 * time.Second}.Run(1, qload.Trigger{}, 200*time.Second)
	if r := closed.Rate("served", 0, 200*time.Second); r < 1.5 || r > 2.2 {
		t.Errorf("4 users thinking 2 s: goodput %.2f/s, want about 2", r)
	}
	if q := closed.Last("queue"); q > 4 {
		t.Errorf("4 users, %d requests at the service", q)
	}
}
