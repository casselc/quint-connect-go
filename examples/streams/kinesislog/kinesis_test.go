package kinesislog_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/casselc/quint-connect-go/examples/streams/kinesisfake"
	"github.com/casselc/quint-connect-go/examples/streams/kinesislog"
	"github.com/casselc/quint-connect-go/examples/streams/pipeline"
	"github.com/casselc/quint-connect-go/examples/streams/tier"
	"github.com/casselc/quint-connect-go/qobs"
	"github.com/casselc/quint-connect-go/qtest"
	"github.com/casselc/quint-connect-go/qtrace"
)

const binding = "../streams.binding.yaml"

// run runs the pipeline with m on the fake Kinesis endpoint (two shards)
// through the AWS SDK, one consumer owning both shards, its steps
// recorded.
func run(t *testing.T, m pipeline.Mutation) ([]qtrace.Step, *tier.Result) {
	t.Helper()
	srv := kinesisfake.New("events", 2)
	defer srv.Close()
	c := kinesislog.Client(srv.URL)
	mem := &qobs.MemorySink{}
	ctx := qobs.WithRecorder(context.Background(), qobs.NewRecorder(mem))
	cps := &kinesislog.Checkpoints{}
	res, err := tier.Run(ctx, tier.Config{Mutation: m, Consumers: 1, Timeout: 15 * time.Second}, &kinesislog.Writer{C: c, Stream: "events"},
		func(int) (pipeline.Reader, func(), error) {
			return &kinesislog.Reader{C: c, Stream: "events", Checkpoints: cps}, func() {}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	return mem.Steps(), res
}

// The faithful pipeline on Kinesis, checked by the same model and binding
// as on Kafka and in simulation.
func TestKinesisConforms(t *testing.T) {
	qtest.RequireQuint(t)
	steps, res := run(t, pipeline.Faithful)
	if !res.Complete {
		t.Fatalf("the projection is incomplete after the timeout")
	}
	for _, k := range res.Keys {
		if got := pipeline.Total(context.Background(), res.Projection, k); got != 1+2+3 {
			t.Errorf("%s: total %d, want 6", k, got)
		}
	}
	t.Logf("%d steps", len(steps))
	qtest.Validate(t, binding, steps).Expect(t, qtest.Conforms())
}

// The bugs a fault-free run shows, on Kinesis too.
func TestKinesisMutants(t *testing.T) {
	qtest.RequireQuint(t)
	for _, m := range []pipeline.Mutation{pipeline.CommitFirst, pipeline.OffByOne} {
		t.Run(fmt.Sprint(m), func(t *testing.T) {
			steps, _ := run(t, m)
			qtest.Validate(t, binding, steps).Expect(t, qtest.Violates("committedApplied"))
		})
	}
}
