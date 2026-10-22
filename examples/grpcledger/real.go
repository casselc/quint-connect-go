package grpcledger

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/casselc/quint-connect-go/examples/grpcledger/ledger"
	"github.com/casselc/quint-connect-go/examples/grpcledger/ledgerpb"
)

// RealConfig serves the ledger for real: a grpc.Server instrumented with
// otelgrpc, over an in-memory listener, and a client whose interceptor
// injects the network's faults.
type RealConfig struct {
	Mutation ledger.Mutation
	// Tracer receives the server's spans (otelgrpc), which
	// ledger.convert.yaml turns into steps.
	Tracer trace.TracerProvider
	// Faults the client's interceptor injects, by a generator seeded with
	// Seed: Refuse fails a call with Unavailable before it is sent,
	// LoseResponse with DeadlineExceeded after the server answered (the
	// transfer applied, and the client cannot tell).
	Seed                 uint64
	Refuse, LoseResponse float64
}

// Serve starts the ledger and returns a client of it, and a function that
// stops both.
func Serve(cfg RealConfig) (*ledger.Client, func(), error) {
	db := ledger.NewMemDB()
	ledger.Open(db, Initial)
	srv := grpc.NewServer(grpc.StatsHandler(otelgrpc.NewServerHandler(otelgrpc.WithTracerProvider(cfg.Tracer))))
	ledgerpb.RegisterLedgerServer(srv, &ledger.Server{DB: db, Mutation: cfg.Mutation})
	lis := bufconn.Listen(1 << 20)
	go func() { _ = srv.Serve(lis) }()
	r := rand.New(rand.NewPCG(cfg.Seed, 0))
	var mu sync.Mutex
	inject := func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		mu.Lock()
		refuse, lose := r.Float64() < cfg.Refuse, r.Float64() < cfg.LoseResponse
		mu.Unlock()
		if refuse {
			return status.Error(codes.Unavailable, "injected: connection refused")
		}
		err := invoker(ctx, method, req, reply, cc, opts...)
		if err == nil && lose {
			return status.Error(codes.DeadlineExceeded, "injected: response lost")
		}
		return err
	}
	conn, err := grpc.NewClient("passthrough:///ledger",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(inject))
	if err != nil {
		srv.Stop()
		return nil, nil, err
	}
	// GracefulStop waits for every handler, and so for its span to end:
	// Stop cancels what is still being served, and a request that committed
	// before otelgrpc ended its span would lose its step
	stop := func() { srv.GracefulStop(); _ = conn.Close() }
	return &ledger.Client{Ledger: ledgerpb.NewLedgerClient(conn), Backoff: 5 * time.Millisecond}, stop, nil
}

// Load runs clients concurrently against a ledger, each making transfers
// between random accounts of 1 to Initial (seeded per client), and a
// balance read after some.
func Load(ctx context.Context, cl *ledger.Client, clients, transfers int, seed uint64) {
	var wg sync.WaitGroup
	for c := 1; c <= clients; c++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := rand.New(rand.NewPCG(seed, uint64(c)))
			accts := ledger.Accounts
			for i := 1; i <= transfers; i++ {
				src := r.IntN(len(accts))
				dest := (src + 1 + r.IntN(len(accts)-1)) % len(accts)
				_, _ = cl.Transfer(ctx, fmt.Sprintf("c%d-%d", c, i), accts[src], accts[dest], int64(1+r.IntN(Initial)))
				if r.IntN(2) == 1 {
					_, _ = cl.Balance(ctx, accts[r.IntN(len(accts))])
				}
			}
		}()
	}
	wg.Wait()
}
