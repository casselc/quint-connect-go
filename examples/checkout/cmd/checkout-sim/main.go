// Command checkout-sim runs the shop in simulated environments, one per
// seed, and writes each run's model steps to <out>/seed<N>.jsonl for
// `quintgo validate`.
//
//	checkout-sim -seeds 20 -isolation snapshot -mutation lock-without-write -out /tmp/runs
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/casselc/quint-connect-go/examples/checkout"
	"github.com/casselc/quint-connect-go/examples/checkout/shop"
	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qobs"
)

func main() {
	seeds := flag.Int("seeds", 20, "runs, with seeds 1..N")
	iso := flag.String("isolation", "serializable", "serializable | snapshot | read-committed")
	mut := flag.String("mutation", "", "no-locks | lock-without-write | no-idempotency-key | dual-write")
	crash := flag.Float64("crash-rate", 0.005, "probability of a crash at each scheduling step")
	out := flag.String("out", "runs", "directory for the step logs")
	flag.Parse()
	cfg := checkout.Config{
		Isolation: map[string]qenv.Isolation{"serializable": qenv.Serializable, "snapshot": qenv.SnapshotIsolation, "read-committed": qenv.ReadCommitted}[*iso],
		Mutation: map[string]shop.Mutation{"": shop.Faithful, "no-locks": shop.NoLocks, "lock-without-write": shop.LockWithoutWrite,
			"no-idempotency-key": shop.NoIdempotencyKey, "dual-write": shop.DualWrite}[*mut],
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
		c.Rates.Kinds = map[string]float64{"fault.nemesis": *crash}
		// the run records through its simulation's context
		rec := qobs.NewRecorder(sink, qobs.WithActor(fmt.Sprintf("seed%d", seed)))
		if _, err := checkout.Run(qenv.WithContext(c, qobs.WithRecorder(context.Background(), rec)), cfg); err != nil {
			fail(err)
		}
		if err := sink.Close(); err != nil {
			fail(err)
		}
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "checkout-sim:", err)
	os.Exit(2)
}
