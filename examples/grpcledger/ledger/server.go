// Package ledger is a gRPC ledger service (../proto/ledger.proto): money
// moves between accounts by Transfer, idempotent by key, and Balance reads
// an account. The server keeps its state in a transactional store behind
// qenv.DB: a qenv.Store in a simulation, MemDB (or an adapter over a SQL
// database) for real.
//
// Each request's effect is recorded as a model step (../model/ledger.qnt)
// at the commit where it takes effect (Txn.OnCommit), twice over: as a
// qobs step, which a simulation's recorder takes, and as attributes of the
// request's OTel span (the server span otelgrpc starts), which
// ../ledger.convert.yaml turns into the same steps. A step is recorded at
// the server, not by the client on return: a transfer applied before its
// response was lost happened, whether or not the client ever learns it.
package ledger

import (
	"context"
	"errors"
	"sync/atomic"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/casselc/quint-connect-go/examples/grpcledger/ledgerpb"
	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qobs"
)

// Mutation injects a bug.
type Mutation int

const (
	Faithful Mutation = iota
	// NoDedupe never looks up the idempotency key: a retry after a lost
	// response, or a duplicated request, applies the transfer again. Only
	// faults show it: without them every key arrives once.
	NoDedupe
	// NoFundsCheck moves money the source does not have. Any workload
	// that overdraws shows it, faults or none.
	NoFundsCheck
	// SplitTxn reads the balances in one transaction and writes the new
	// ones in another: a transfer committed in between is overwritten (a
	// lost update), at any isolation level, since the second transaction
	// reads nothing it could conflict on.
	SplitTxn
)

func (m Mutation) String() string {
	return [...]string{"Faithful", "NoDedupe", "NoFundsCheck", "SplitTxn"}[m]
}

// Accounts are the ledger's accounts (the model's ACCOUNTS).
var Accounts = []string{"a", "b", "c"}

// Server implements ledgerpb.LedgerServer.
type Server struct {
	ledgerpb.UnimplementedLedgerServer
	DB       qenv.DB
	Mutation Mutation

	// seq numbers commits that record a step, in commit order: the spans'
	// order (ledger.seq), since spans of concurrent requests end in any
	// order.
	seq atomic.Int64
}

// Open loads every account with initial (a migration, before the server
// starts).
func Open(db interface{ Load(key string, val any) }, initial int64) {
	for _, a := range Accounts {
		db.Load("acct/"+a, initial)
	}
}

// applied is an idempotency record: the transfer a key applied and the
// response it got.
type applied struct {
	Src, Dest       string
	Amount          int64
	SrcBal, DestBal int64
}

var errInsufficient = status.Error(codes.FailedPrecondition, "insufficient funds")

// Transfer moves amount from src to dest once per idempotency key. A key
// already applied gets the first response again (replayed); a key reused
// for another transfer is an error.
func (s *Server) Transfer(ctx context.Context, req *ledgerpb.TransferRequest) (*ledgerpb.TransferResponse, error) {
	key, src, dest, amount := req.GetIdempotencyKey(), req.GetFrom(), req.GetTo(), req.GetAmount()
	if key == "" || !known(src) || !known(dest) || src == dest || amount <= 0 {
		return nil, status.Errorf(codes.InvalidArgument, "transfer %q: want a key, two distinct accounts of %v and a positive amount", key, Accounts)
	}
	var resp *ledgerpb.TransferResponse
	var write bool // SplitTxn: the write is left for a second transaction
	var sb, db int64
	err := qenv.Retry(ctx, s.DB, 10, func(tx qenv.Txn) error {
		resp, write = nil, false
		if s.Mutation != NoDedupe {
			if v, ok, err := tx.GetForUpdate("idem/" + key); err != nil {
				return err
			} else if ok {
				a := v.(applied)
				if a.Src != src || a.Dest != dest || a.Amount != amount {
					return status.Errorf(codes.AlreadyExists, "key %q was used for another transfer", key)
				}
				resp = &ledgerpb.TransferResponse{FromBalance: a.SrcBal, ToBalance: a.DestBal, Replayed: true}
				tx.OnCommit(func() { s.record(ctx, "replay", key, src, dest, amount, nil) })
				return nil
			}
		}
		// lock the two accounts in one order: a→b and b→a, each holding
		// its first lock, would wait for each other forever (the first
		// draft did; a simulation run that recorded nothing showed it)
		first, second := &sb, &db
		if dest < src {
			first, second = &db, &sb
		}
		var err error
		if *first, err = balance(tx, min(src, dest)); err != nil {
			return err
		}
		if *second, err = balance(tx, max(src, dest)); err != nil {
			return err
		}
		if s.Mutation != NoFundsCheck && sb < amount {
			tx.OnCommit(func() { s.record(ctx, "refuse", key, src, dest, amount, &[2]int64{sb, db}) })
			return nil
		}
		sb, db = sb-amount, db+amount
		resp = &ledgerpb.TransferResponse{FromBalance: sb, ToBalance: db}
		if s.Mutation == SplitTxn {
			write = true
			return nil
		}
		s.apply(ctx, tx, key, src, dest, amount, sb, db)
		return nil
	})
	if err == nil && write {
		// SplitTxn: the balances computed above are written blind, and a
		// transfer committed since is overwritten
		err = qenv.Retry(ctx, s.DB, 10, func(tx qenv.Txn) error {
			s.apply(ctx, tx, key, src, dest, amount, sb, db)
			return nil
		})
	}
	if err != nil {
		return nil, storeErr(err)
	}
	if resp == nil {
		return nil, errInsufficient
	}
	return resp, nil
}

// apply writes a transfer's balances and its idempotency record, and
// records its step at the commit.
func (s *Server) apply(ctx context.Context, tx qenv.Txn, key, src, dest string, amount, sb, db int64) {
	tx.Put("acct/"+src, sb)
	tx.Put("acct/"+dest, db)
	tx.Put("idem/"+key, applied{src, dest, amount, sb, db})
	tx.OnCommit(func() { s.record(ctx, "transfer", key, src, dest, amount, &[2]int64{sb, db}) })
}

// Balance reads an account.
func (s *Server) Balance(ctx context.Context, req *ledgerpb.BalanceRequest) (*ledgerpb.BalanceResponse, error) {
	acct := req.GetAccount()
	if !known(acct) {
		return nil, status.Errorf(codes.InvalidArgument, "balance: no account %q (accounts: %v)", acct, Accounts)
	}
	var n int64
	err := qenv.Retry(ctx, s.DB, 10, func(tx qenv.Txn) error {
		var err error
		n, err = balance(tx, acct)
		tx.OnCommit(func() {
			qobs.Record(ctx, "read", nil, "account", acct, "obs.amount", n)
			trace.SpanFromContext(ctx).SetAttributes(attribute.String("ledger.action", "read"),
				attribute.String("ledger.account", acct), attribute.Int64("ledger.amount", n),
				attribute.Int64("ledger.seq", s.seq.Add(1)))
		})
		return err
	})
	if err != nil {
		return nil, storeErr(err)
	}
	return &ledgerpb.BalanceResponse{Amount: n}, nil
}

// record records a transfer request's step: the qobs step and the span's
// attributes, with the source's and destination's balances if observed
// (an overdraft's are negative, so no sentinel value would do).
func (s *Server) record(ctx context.Context, action, key, src, dest string, amount int64, bal *[2]int64) {
	kv := []any{"key", key, "src", src, "dest", dest, "amount", amount}
	attrs := []attribute.KeyValue{attribute.String("ledger.action", action), attribute.String("ledger.key", key),
		attribute.String("ledger.src", src), attribute.String("ledger.dest", dest), attribute.Int64("ledger.amount", amount),
		attribute.Int64("ledger.seq", s.seq.Add(1))}
	if bal != nil {
		kv = append(kv, "obs.src_balance", bal[0], "obs.dest_balance", bal[1])
		attrs = append(attrs, attribute.Int64("ledger.src_balance", bal[0]), attribute.Int64("ledger.dest_balance", bal[1]))
	}
	qobs.Record(ctx, action, nil, kv...)
	trace.SpanFromContext(ctx).SetAttributes(attrs...)
}

func balance(tx qenv.Txn, acct string) (int64, error) {
	v, ok, err := tx.GetForUpdate("acct/" + acct)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, status.Errorf(codes.Internal, "account %s not loaded", acct)
	}
	return v.(int64), nil
}

func known(acct string) bool {
	for _, a := range Accounts {
		if a == acct {
			return true
		}
	}
	return false
}

// storeErr maps a store error to a status: a conflict that persisted is
// Aborted (retry), an unknown commit outcome Unavailable; a status passes.
func storeErr(err error) error {
	if _, ok := status.FromError(err); ok {
		return err
	}
	if errors.Is(err, qenv.ErrConflict) {
		return status.Error(codes.Aborted, err.Error())
	}
	return status.Error(codes.Unavailable, err.Error())
}
