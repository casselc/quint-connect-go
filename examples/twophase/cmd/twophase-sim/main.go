// Command twophase-sim runs two-phase commit in simulated environments,
// one per seed, and writes each run's model steps to <out>/seed<N>.jsonl
// for `quintgo validate` against the Choreo model.
//
//	twophase-sim -seeds 20 -mutation impatient -out /tmp/runs
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/casselc/quint-connect-go/examples/twophase"
	"github.com/casselc/quint-connect-go/examples/twophase/tpc"
	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qobs"
)

func main() {
	seeds := flag.Int("seeds", 20, "runs, with seeds 1..N")
	mut := flag.String("mutation", "", "early-commit | impatient | volatile-decision")
	rate := flag.Float64("fault-rate", 0.2, "probability of each message fault")
	crash := flag.Float64("crash-rate", 0.05, "probability of a crash at each scheduling step")
	out := flag.String("out", "runs", "directory for the step logs")
	flag.Parse()
	m, ok := map[string]tpc.Mutation{"": tpc.Faithful, "early-commit": tpc.EarlyCommit, "impatient": tpc.ImpatientParticipant,
		"volatile-decision": tpc.VolatileDecision}[*mut]
	if !ok {
		fail(fmt.Errorf("unknown mutation %q", *mut))
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fail(err)
	}
	for seed := 1; seed <= *seeds; seed++ {
		// durable: a crash is part of the run, and the log must keep its
		// last steps
		sink, err := qobs.FileSink(filepath.Join(*out, fmt.Sprintf("seed%d.jsonl", seed)))
		if err != nil {
			fail(err)
		}
		c := qenv.NewSeeded(uint64(seed))
		c.Rates = qenv.Rates{Default: *rate, Kinds: map[string]float64{"nemesis": *crash}}
		rec := qobs.NewRecorder(sink, qobs.WithActor(fmt.Sprintf("seed%d", seed)))
		if _, err := twophase.Run(qenv.WithContext(c, qobs.WithRecorder(context.Background(), rec)), twophase.Config{Mutation: m}); err != nil {
			fail(err)
		}
		if err := sink.Close(); err != nil {
			fail(err)
		}
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "twophase-sim:", err)
	os.Exit(2)
}
