// Package grpcledger deploys the ledger's gRPC service (package ledger) into
// a qenv simulation: the generated server and client unchanged, connected
// through simgrpc, the server a crashable process over a transactional
// store, and clients that retry with the same idempotency key. Every
// choice of that environment (which request is lost, duplicated or refused,
// when the server crashes, how transactions interleave, what clients ask
// for) is made by one qenv.Chooser: a seed, Hegel, or a replay.
package grpcledger

import (
	"context"
	"fmt"
	"time"

	"github.com/casselc/quint-connect-go/examples/grpcledger/ledger"
	"github.com/casselc/quint-connect-go/examples/grpcledger/ledgerpb"
	"github.com/casselc/quint-connect-go/examples/grpcledger/simgrpc"
	"github.com/casselc/quint-connect-go/qenv"
)

// Initial is every account's opening balance (the binding's INITIAL).
const Initial = 5

// Config of a deployment and its workload.
type Config struct {
	Mutation ledger.Mutation
	// Clients (default 3) each make Transfers (default 3) between random
	// accounts, of 1 to Initial: enough to overdraw an account.
	Clients, Transfers int
	// NoCrashes keeps the server up. CrashAfterCommit lets a crash fall
	// between a transfer's commit and its response (simgrpc).
	NoCrashes        bool
	CrashAfterCommit bool
}

// Deployment is a running ledger in a simulation.
type Deployment struct {
	Sim    *qenv.Sim
	Server *simgrpc.Server
	DB     *qenv.Store
	Client *ledger.Client
}

// Deploy starts the ledger server in s.
func Deploy(s *qenv.Sim, cfg Config) *Deployment {
	db := qenv.NewStore(s, "ledger", qenv.Serializable)
	ledger.Open(db, Initial)
	proc := s.Process("ledger", !cfg.NoCrashes, func(context.Context) {})
	srv := simgrpc.NewServer(s, "ledger", proc)
	srv.CrashAfterCommit(cfg.CrashAfterCommit)
	ledgerpb.RegisterLedgerServer(srv, &ledger.Server{DB: db, Mutation: cfg.Mutation})
	return &Deployment{Sim: s, Server: srv, DB: db, Client: &ledger.Client{Ledger: ledgerpb.NewLedgerClient(srv.Dial())}}
}

// Workload starts the clients. Each transfer's accounts and amount, a
// pause before it, and a balance read after it are the chooser's.
func (d *Deployment) Workload(cfg Config) {
	clients, transfers := cfg.Clients, cfg.Transfers
	if clients == 0 {
		clients = 3
	}
	if transfers == 0 {
		transfers = 3
	}
	accts := ledger.Accounts
	for c := 1; c <= clients; c++ {
		d.Sim.Spawn(fmt.Sprintf("client%d", c), func(ctx context.Context) {
			for i := 1; i <= transfers; i++ {
				qenv.Sleep(ctx, time.Duration(d.Sim.Choose("client.pause", 4))*100*time.Millisecond)
				src := d.Sim.Choose("client.src", len(accts))
				dest := (src + 1 + d.Sim.Choose("client.dest", len(accts)-1)) % len(accts)
				amount := int64(1 + d.Sim.Choose("client.amount", Initial))
				_, _ = d.Client.Transfer(ctx, fmt.Sprintf("c%d-%d", c, i), accts[src], accts[dest], amount)
				if d.Sim.Choose("client.read", 2) == 1 {
					_, _ = d.Client.Balance(ctx, accts[d.Sim.Choose("client.account", len(accts))])
				}
			}
		})
	}
}

// Run deploys the ledger with c making the choices, runs the workload for
// up to 30 s of virtual time with faults, and settles for up to 30 s
// without.
func Run(c qenv.Chooser, cfg Config) (*Deployment, error) {
	s := qenv.New(c)
	defer s.Close()
	d := Deploy(s, cfg)
	d.Workload(cfg)
	if err := s.Run(30 * time.Second); err != nil {
		return d, err
	}
	return d, s.Settle(30 * time.Second)
}
