// Command dbaas-server serves the dbaas cloud over HTTP, with fault
// injection, and records its steps (dbCreated, dbPassword, dbDeleted) to a
// durable step log, ordered by wall clock (order.wall) so they merge with
// the driver's.
//
//	dbaas-server -listen :8085 -steps cloud.jsonl -drop-response 0.2 -visibility-lag 2s
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/casselc/quint-connect-go/examples/crossdb/dbaas"
	"github.com/casselc/quint-connect-go/qobs"
)

func main() {
	listen := flag.String("listen", ":8085", "address")
	steps := flag.String("steps", "cloud.jsonl", "step log")
	drop := flag.Float64("drop-response", 0, "probability a mutating request is applied and its response dropped")
	throttle := flag.Float64("throttle-rps", 0, "requests per second before 429s (0: none)")
	lag := flag.Duration("visibility-lag", time.Second, "how long a new database is invisible to reads")
	seed := flag.Uint64("seed", 1, "fault seed")
	flag.Parse()
	sink, err := qobs.FileSink(*steps)
	if err != nil {
		fail(err)
	}
	rec := qobs.NewRecorder(sink, qobs.WithActor("crossdb"), qobs.WithProcess("crossdb"))
	cloud := dbaas.NewCloud()
	cloud.VisibilityLag = *lag
	cloud.Record = func(ctx context.Context, action string, kv ...any) {
		rec.Record(ctx, action, nil, append(kv, "thread", "cloud", "order.wall", time.Now().UnixNano())...)
	}
	srv := &dbaas.Server{Cloud: cloud, Faults: dbaas.Faults{DropResponse: *drop, ThrottleRPS: *throttle, Seed: *seed}}
	fmt.Fprintf(os.Stderr, "dbaas-server on %s (drop %.2f, throttle %.0f rps, lag %v)\n", *listen, *drop, *throttle, *lag)
	if err := http.ListenAndServe(*listen, srv.Handler()); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "dbaas-server:", err)
	os.Exit(1)
}
