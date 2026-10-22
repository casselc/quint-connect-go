package kafkalog_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/casselc/quint-connect-go/examples/streams/kafkalog"
	"github.com/casselc/quint-connect-go/examples/streams/pipeline"
	"github.com/casselc/quint-connect-go/examples/streams/tier"
	"github.com/casselc/quint-connect-go/qobs"
	"github.com/casselc/quint-connect-go/qtest"
	"github.com/casselc/quint-connect-go/qtrace"
	"github.com/twmb/franz-go/pkg/kfake"
)

const binding = "../streams.binding.yaml"

// run runs the pipeline with m on an in-process Kafka cluster (kfake, two
// partitions), its steps recorded.
func run(t *testing.T, m pipeline.Mutation) ([]qtrace.Step, *tier.Result) {
	t.Helper()
	c, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.SeedTopics(2, "events"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	brokers := c.ListenAddrs()
	w, err := kafkalog.NewWriter(brokers, "events", m != pipeline.NonIdempotentProducer)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	mem := &qobs.MemorySink{}
	ctx := qobs.WithRecorder(context.Background(), qobs.NewRecorder(mem))
	res, err := tier.Run(ctx, tier.Config{Mutation: m, Timeout: 15 * time.Second}, w, func(int) (pipeline.Reader, func(), error) {
		r, err := kafkalog.NewReader(brokers, "events", "projection")
		if err != nil {
			return nil, nil, err
		}
		return r, r.Close, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return mem.Steps(), res
}

// The faithful pipeline on Kafka: two consumers of one group (the group
// protocol assigns them the partitions), checked by the same model and
// binding as the simulated runs.
func TestKafkaConforms(t *testing.T) {
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

// The bugs a fault-free run shows on Kafka too: a commit ahead of its
// applies, and each partition's first record skipped.
func TestKafkaMutants(t *testing.T) {
	qtest.RequireQuint(t)
	for _, m := range []pipeline.Mutation{pipeline.CommitFirst, pipeline.OffByOne} {
		t.Run(fmt.Sprint(m), func(t *testing.T) {
			steps, _ := run(t, m)
			qtest.Validate(t, binding, steps).Expect(t, qtest.Violates("committedApplied"))
		})
	}
}
