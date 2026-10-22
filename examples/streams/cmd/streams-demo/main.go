// Command streams-demo runs the pipeline and writes each run's model steps
// to <out>/<backend>-<N>.jsonl for `quintgo validate`: simulated runs, one
// per seed, or runs on an in-process Kafka cluster (kfake) or a fake
// Kinesis endpoint through the real clients.
//
//	streams-demo -backend sim -seeds 20 -mutation no-dedupe -out /tmp/runs
//	streams-demo -backend kafka -runs 2 -out /tmp/runs
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/casselc/quint-connect-go/examples/streams"
	"github.com/casselc/quint-connect-go/examples/streams/kafkalog"
	"github.com/casselc/quint-connect-go/examples/streams/kinesisfake"
	"github.com/casselc/quint-connect-go/examples/streams/kinesislog"
	"github.com/casselc/quint-connect-go/examples/streams/pipeline"
	"github.com/casselc/quint-connect-go/examples/streams/tier"
	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qobs"
	"github.com/twmb/franz-go/pkg/kfake"
)

var mutations = map[string]pipeline.Mutation{"": pipeline.Faithful, "commit-first": pipeline.CommitFirst, "no-dedupe": pipeline.NoDedupe,
	"non-idempotent-producer": pipeline.NonIdempotentProducer, "off-by-one": pipeline.OffByOne}

func main() {
	backend := flag.String("backend", "sim", "sim | kafka | kinesis")
	runs := flag.Int("runs", 20, "runs (sim: seeds 1..N)")
	mut := flag.String("mutation", "", "commit-first | no-dedupe | non-idempotent-producer | off-by-one")
	rate := flag.Float64("fault-rate", 0.1, "sim: probability of a fault at each fault choice")
	out := flag.String("out", "runs", "directory for the step logs")
	flag.Parse()
	m, ok := mutations[*mut]
	if !ok {
		fail(fmt.Errorf("unknown mutation %q", *mut))
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fail(err)
	}
	for i := 1; i <= *runs; i++ {
		name := fmt.Sprintf("%s-%d", *backend, i)
		sink, err := qobs.FileSink(filepath.Join(*out, name+".jsonl"))
		if err != nil {
			fail(err)
		}
		ctx := qobs.WithRecorder(context.Background(), qobs.NewRecorder(sink, qobs.WithActor(name)))
		switch *backend {
		case "sim":
			c := qenv.NewSeeded(uint64(i))
			c.Rates = qenv.Rates{Default: *rate, Kinds: map[string]float64{"nemesis": *rate / 2}}
			_, err = streams.Run(qenv.WithContext(c, ctx), streams.Config{Mutation: m})
		case "kafka":
			err = runKafka(ctx, m)
		case "kinesis":
			err = runKinesis(ctx, m)
		default:
			err = fmt.Errorf("unknown backend %q", *backend)
		}
		if err != nil {
			fail(err)
		}
		if err := sink.Close(); err != nil {
			fail(err)
		}
	}
}

func runKafka(ctx context.Context, m pipeline.Mutation) error {
	c, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.SeedTopics(2, "events"))
	if err != nil {
		return err
	}
	defer c.Close()
	w, err := kafkalog.NewWriter(c.ListenAddrs(), "events", m != pipeline.NonIdempotentProducer)
	if err != nil {
		return err
	}
	defer w.Close()
	_, err = tier.Run(ctx, tier.Config{Mutation: m}, w, func(int) (pipeline.Reader, func(), error) {
		r, err := kafkalog.NewReader(c.ListenAddrs(), "events", "projection")
		if err != nil {
			return nil, nil, err
		}
		return r, r.Close, nil
	})
	return err
}

func runKinesis(ctx context.Context, m pipeline.Mutation) error {
	srv := kinesisfake.New("events", 2)
	defer srv.Close()
	c := kinesislog.Client(srv.URL)
	cps := &kinesislog.Checkpoints{}
	_, err := tier.Run(ctx, tier.Config{Mutation: m, Consumers: 1}, &kinesislog.Writer{C: c, Stream: "events"},
		func(int) (pipeline.Reader, func(), error) {
			return &kinesislog.Reader{C: c, Stream: "events", Checkpoints: cps}, func() {}, nil
		})
	return err
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "streams-demo:", err)
	os.Exit(2)
}
