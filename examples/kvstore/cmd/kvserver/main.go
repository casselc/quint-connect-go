// Command kvserver runs the kvstore example service, recording what it does
// for checking against model/kv.qnt:
//
//	kvserver -addr :8080 -steps steps.jsonl -spans spans.jsonl
//
// -steps writes the store's model steps (qobs, native step log); -spans
// writes the server's ordinary OTel spans as OTLP/JSON, as a collector's
// file exporter would. Stop it with SIGINT/SIGTERM to flush both, then
//
//	quintgo validate -binding kv.binding.yaml steps.jsonl
//	quintgo validate -binding kv.binding.yaml -convert kv.convert.yaml spans.jsonl
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/casselc/quint-connect-go/examples/kvstore/kv"
	"github.com/casselc/quint-connect-go/otelio"
	"github.com/casselc/quint-connect-go/qobs"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "listen address")
	steps := flag.String("steps", "", "write model steps (native step log) here")
	spans := flag.String("spans", "", "write OTel spans (OTLP/JSON) here")
	mutation := flag.String("mutation", "", "inject a bug: stale-cas | revision-on-conflict")
	flag.Parse()

	m := kv.Faithful
	switch *mutation {
	case "":
	case "stale-cas":
		m = kv.StaleCAS
	case "revision-on-conflict":
		m = kv.RevisionOnConflict
	default:
		log.Fatalf("unknown mutation %q", *mutation)
	}

	if *steps != "" {
		sink, err := qobs.FileSink(*steps)
		if err != nil {
			log.Fatal(err)
		}
		defer sink.Close()
		qobs.SetGlobal(qobs.NewRecorder(sink, qobs.WithActor("kv"), qobs.WithProcess("kvserver")))
	}
	var tp trace.TracerProvider
	if *spans != "" {
		f, err := os.Create(*spans)
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()
		sdk := sdktrace.NewTracerProvider(
			sdktrace.WithBatcher(otelio.NewFileExporter(f)),
			sdktrace.WithSampler(sdktrace.AlwaysSample()), // a sampled-out span is a step lost
			sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", "kvserver"))))
		defer sdk.Shutdown(context.Background())
		tp = sdk
	}

	srv := &http.Server{Handler: kv.NewServer(kv.NewStore(m), tp).Handler()}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("kvserver listening on %s (mutation %q)", ln.Addr(), *mutation)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		_ = srv.Shutdown(context.Background())
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	log.Print("kvserver stopped; flushing")
}
