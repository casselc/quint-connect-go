// Command hello-sim runs the counter and its client in simulated
// environments, one per seed, and writes each run's model steps to
// <out>/seed<N>.jsonl for `quintgo validate`.
//
//	hello-sim -seeds 20 -mutation no-dedupe -out /tmp/runs
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/casselc/quint-connect-go/examples/hello"
	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qobs"
)

func main() {
	seeds := flag.Int("seeds", 20, "runs, with seeds 1..N")
	mut := flag.String("mutation", "", "no-dedupe")
	rate := flag.Float64("fault-rate", 0.2, "probability of each message fault")
	out := flag.String("out", "runs", "directory for the step logs")
	flag.Parse()
	m, ok := map[string]hello.Mutation{"": hello.Faithful, "no-dedupe": hello.NoDedupe}[*mut]
	if !ok {
		fail(fmt.Errorf("unknown mutation %q (want no-dedupe)", *mut))
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fail(err)
	}
	for seed := 1; seed <= *seeds; seed++ {
		sink, err := qobs.FileSink(filepath.Join(*out, fmt.Sprintf("seed%d.jsonl", seed)))
		if err != nil {
			fail(err)
		}
		c := qenv.NewSeeded(uint64(seed))
		c.Rates = qenv.Rates{Default: *rate}
		rec := qobs.NewRecorder(sink, qobs.WithActor(fmt.Sprintf("seed%d", seed)))
		if err := hello.Run(qenv.WithContext(c, qobs.WithRecorder(context.Background(), rec)), m); err != nil {
			fail(err)
		}
		if err := sink.Close(); err != nil {
			fail(err)
		}
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "hello-sim:", err)
	os.Exit(2)
}
