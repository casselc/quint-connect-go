// Command ledger-real serves the ledger for real (a grpc.Server with
// otelgrpc, an injecting client interceptor), runs a concurrent load, and
// writes the server's spans as OTLP/JSON, for
// `quintgo validate -convert ledger.convert.yaml`: a production service's
// check, from the spans it already exports.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/casselc/quint-connect-go/examples/grpcledger"
	"github.com/casselc/quint-connect-go/examples/grpcledger/ledger"
	"github.com/casselc/quint-connect-go/otelio"
)

func main() {
	mutation := flag.String("mutation", "", "inject a bug: no-dedupe, no-funds-check or split-txn")
	seed := flag.Uint64("seed", 1, "seed of the load and the injected faults")
	refuse := flag.Float64("refuse", 0.1, "probability a call is refused before it is sent")
	lose := flag.Float64("lose", 0.2, "probability a response is lost after the server answered")
	clients := flag.Int("clients", 4, "concurrent clients")
	transfers := flag.Int("transfers", 8, "transfers per client")
	out := flag.String("out", "spans.json", "OTLP/JSON spans to write")
	flag.Parse()
	m, ok := map[string]ledger.Mutation{"": ledger.Faithful, "no-dedupe": ledger.NoDedupe,
		"no-funds-check": ledger.NoFundsCheck, "split-txn": ledger.SplitTxn}[*mutation]
	if !ok {
		fmt.Fprintf(os.Stderr, "ledger-real: unknown -mutation %q (want no-dedupe, no-funds-check or split-txn)\n", *mutation)
		os.Exit(2)
	}
	f, err := os.Create(*out)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer f.Close()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(otelio.NewFileExporter(f)))
	cl, stop, err := grpcledger.Serve(grpcledger.RealConfig{Mutation: m, Tracer: tp, Seed: *seed, Refuse: *refuse, LoseResponse: *lose})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	grpcledger.Load(context.Background(), cl, *clients, *transfers, *seed)
	stop()
	_ = tp.Shutdown(context.Background())
}
