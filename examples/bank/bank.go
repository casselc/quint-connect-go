// Package bank is the system under test of the examples: a small in-memory
// bank, checked against model/bank.qnt every way quint-connect-go supports:
//
//	mbt_test.go     the model drives the code (connect.Run): random model
//	                traces are replayed on the bank, states compared per step
//	hegel_test.go   Hegel drives the code (quinthegel): state-machine tests
//	                whose runs the model must accept; model traces replayed
//	                inside Hegel, with Hegel drawing the concrete details
//	trace_test.go   the code reports what it did: model steps recorded with
//	                qobs (qtest), and the bank's ordinary OTel spans
//	                converted to steps (convert), both checked by the model
//
// The bank is instrumented two ways, as a real service might be:
//   - qobs.Record calls name the model step each operation performs. They
//     cost ~20 ns when no recorder is installed (qobs.SetGlobal);
//   - an OTel span per operation, with the attributes an SRE would want
//     anyway (account, amount, resulting balance). bank.convert.yaml maps
//     those spans to the model's actions, with no quint-specific code.
package bank

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/casselc/quint-connect-go/qobs"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// Errors the bank returns.
var (
	ErrUnknownAccount = errors.New("unknown account")
	ErrInvalidAmount  = errors.New("amount must be positive")
	ErrInsufficient   = errors.New("insufficient funds")
	ErrSameAccount    = errors.New("cannot transfer to the same account")
)

// Mutation injects a bug, for showing what each kind of test catches.
type Mutation int

const (
	Faithful Mutation = iota
	// SelfTransfer accepts a transfer from an account to itself, crediting
	// the amount on top: money from nowhere. No balance goes negative, so
	// a test of non-negativity alone misses it; the model does not.
	SelfTransfer
	// LostDeposit drops every third deposit to an account while reporting
	// success.
	LostDeposit
)

// Bank holds balances. It is safe for concurrent use.
type Bank struct {
	mu       sync.Mutex
	balances map[string]int
	deposits map[string]int
	mutation Mutation
	tracer   trace.Tracer
}

// Option configures a Bank.
type Option func(*Bank)

// WithTracer emits an OTel span per operation.
func WithTracer(tp trace.TracerProvider) Option {
	return func(b *Bank) { b.tracer = tp.Tracer("github.com/casselc/quint-connect-go/examples/bank") }
}

// WithMutation injects a bug.
func WithMutation(m Mutation) Option { return func(b *Bank) { b.mutation = m } }

// New opens the given accounts, all empty.
func New(accounts []string, opts ...Option) *Bank {
	b := &Bank{balances: map[string]int{}, deposits: map[string]int{}, tracer: noop.NewTracerProvider().Tracer("")}
	for _, a := range accounts {
		b.balances[a] = 0
	}
	for _, o := range opts {
		o(b)
	}
	return b
}

// Balance of an account.
func (b *Bank) Balance(account string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.balances[account]
}

// Balances returns a copy of all balances.
func (b *Bank) Balances() map[string]int {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make(map[string]int, len(b.balances))
	for k, v := range b.balances {
		out[k] = v
	}
	return out
}

func (b *Bank) span(ctx context.Context, name string, kv ...attribute.KeyValue) (context.Context, trace.Span) {
	return b.tracer.Start(ctx, "Bank/"+name, trace.WithAttributes(kv...))
}

func end(span trace.Span, err error) {
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
}

// Deposit credits an account.
func (b *Bank) Deposit(ctx context.Context, account string, amount int) (err error) {
	ctx, span := b.span(ctx, "Deposit", attribute.String("bank.account", account), attribute.Int("bank.amount", amount))
	defer func() { end(span, err) }()
	b.mu.Lock()
	defer b.mu.Unlock()
	// The model step, recorded while the lock is held so steps are recorded
	// in the order they took effect. A refused deposit is recorded too, as
	// an error; the binding says the model has no such step (skip).
	defer func() {
		qobs.Record(ctx, "deposit", err, "account", account, "amount", amount, "obs.balance", b.balances[account])
	}()
	if _, ok := b.balances[account]; !ok {
		return ErrUnknownAccount
	}
	if amount <= 0 {
		return ErrInvalidAmount
	}
	b.deposits[account]++
	if !(b.mutation == LostDeposit && b.deposits[account]%3 == 0) {
		b.balances[account] += amount
	}
	span.SetAttributes(attribute.Int("bank.balance", b.balances[account]))
	return nil
}

// Withdraw debits an account if it holds enough.
func (b *Bank) Withdraw(ctx context.Context, account string, amount int) (err error) {
	ctx, span := b.span(ctx, "Withdraw", attribute.String("bank.account", account), attribute.Int("bank.amount", amount))
	defer func() { end(span, err) }()
	b.mu.Lock()
	defer b.mu.Unlock()
	defer func() {
		qobs.Record(ctx, "withdraw", err, "account", account, "amount", amount, "obs.balance", b.balances[account])
	}()
	if _, ok := b.balances[account]; !ok {
		return ErrUnknownAccount
	}
	if amount <= 0 {
		return ErrInvalidAmount
	}
	if b.balances[account] < amount {
		return ErrInsufficient
	}
	b.balances[account] -= amount
	span.SetAttributes(attribute.Int("bank.balance", b.balances[account]))
	return nil
}

// Transfer moves money between two accounts.
func (b *Bank) Transfer(ctx context.Context, src, dst string, amount int) (err error) {
	ctx, span := b.span(ctx, "Transfer", attribute.String("bank.src", src), attribute.String("bank.dst", dst), attribute.Int("bank.amount", amount))
	defer func() { end(span, err) }()
	b.mu.Lock()
	defer b.mu.Unlock()
	defer func() {
		qobs.Record(ctx, "transfer", err, "src", src, "dst", dst, "amount", amount,
			"obs.src_balance", b.balances[src], "obs.dst_balance", b.balances[dst])
	}()
	_, okS := b.balances[src]
	_, okD := b.balances[dst]
	switch {
	case !okS || !okD:
		return ErrUnknownAccount
	case amount <= 0:
		return ErrInvalidAmount
	case src == dst && b.mutation != SelfTransfer:
		return ErrSameAccount
	case b.balances[src] < amount:
		return ErrInsufficient
	}
	b.balances[src] -= amount
	b.balances[dst] += amount
	if src == dst {
		b.balances[dst] += amount // the mutant's bug
	}
	span.SetAttributes(attribute.Int("bank.src_balance", b.balances[src]), attribute.Int("bank.dst_balance", b.balances[dst]))
	return nil
}

func (m Mutation) String() string {
	switch m {
	case Faithful:
		return "faithful"
	case SelfTransfer:
		return "self-transfer"
	case LostDeposit:
		return "lost-deposit"
	}
	return fmt.Sprintf("Mutation(%d)", int(m))
}
