// Package retry is a client-side retry library: a bounded number of
// retries, exponential backoff with jitter, and a retry budget. It is the
// code under test of examples/retrystorm, checked two ways: the budget's
// accounting against model/retry.qnt (qobs steps, Hegel), and the policies'
// effect on a loaded server with qload and qmeta.
package retry

import (
	"context"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/casselc/quint-connect-go/qobs"
)

// Mutation selects a bug in the budget, for the tests that show which check
// catches it.
type Mutation int

const (
	Faithful Mutation = iota
	// Uncapped keeps every token deposited: after a quiet period the
	// bucket holds enough for a burst of retries far over the ratio.
	Uncapped
	// DepositOnRetry counts a retry as a request and deposits for it: the
	// budget partly refills itself.
	DepositOnRetry
)

// Unit is a whole token in milli-tokens: one retry costs Unit.
const Unit = 1000

// A Budget is a token bucket that new requests fill and retries drain
// (Finagle's RetryBudget; gRPC's retry throttling is a relative): over any
// window, retries are at most Ratio/Unit of the requests, plus what the
// bucket holds. Integer milli-tokens, so the model and the code agree
// exactly.
type Budget struct {
	Ratio    int // milli-tokens per request (100: a retry per 10 requests)
	Max      int // the most milli-tokens kept
	Mutation Mutation

	mu     sync.Mutex
	tokens int
}

// Deposit records a new request.
func (b *Budget) Deposit(ctx context.Context) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.add(b.Ratio)
	qobs.Record(ctx, "request", nil, "obs.tokens", b.tokens)
}

func (b *Budget) add(n int) {
	b.tokens += n
	if b.tokens > b.Max && b.Mutation != Uncapped {
		b.tokens = b.Max
	}
}

// Withdraw takes a retry's token, or reports that the budget is exhausted.
func (b *Budget) Withdraw(ctx context.Context) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.tokens < Unit {
		qobs.Record(ctx, "refuse", nil, "obs.tokens", b.tokens)
		return false
	}
	b.tokens -= Unit
	if b.Mutation == DepositOnRetry {
		b.add(b.Ratio)
	}
	qobs.Record(ctx, "retry", nil, "obs.tokens", b.tokens)
	return true
}

// Tokens is the budget's balance in milli-tokens.
func (b *Budget) Tokens() int { b.mu.Lock(); defer b.mu.Unlock(); return b.tokens }

// Policy is a retry policy.
type Policy struct {
	MaxRetries int
	// Backoff is the wait before retry n (1-based); nil retries at once.
	Backoff func(n int) time.Duration
	// Budget, if set, must admit each retry.
	Budget *Budget
}

// Exponential is full-jitter exponential backoff: a uniform wait in
// [0, min(max, base*2^(n-1))).
func Exponential(base, max time.Duration, rnd *rand.Rand) func(n int) time.Duration {
	var mu sync.Mutex
	return func(n int) time.Duration {
		d := base << (n - 1)
		if d > max || d <= 0 {
			d = max
		}
		mu.Lock()
		defer mu.Unlock()
		return time.Duration(rnd.Int64N(int64(d)))
	}
}

// Request is called when a new request starts (qload.Retrier).
func (p *Policy) Request(time.Time) {
	if p.Budget != nil {
		p.Budget.Deposit(context.Background())
	}
}

// Retry decides retry n (qload.Retrier): how long to wait, or false to
// give up.
func (p *Policy) Retry(n int, _ time.Time) (time.Duration, bool) {
	if n > p.MaxRetries {
		return 0, false
	}
	if p.Budget != nil && !p.Budget.Withdraw(context.Background()) {
		return 0, false
	}
	if p.Backoff == nil {
		return 0, true
	}
	return p.Backoff(n), true
}

// Do calls call until it succeeds, the policy gives up, or ctx is done,
// and returns call's last error.
func (p *Policy) Do(ctx context.Context, call func(context.Context) error) error {
	p.Request(time.Now())
	for n := 1; ; n++ {
		err := call(ctx)
		if err == nil || ctx.Err() != nil {
			return err
		}
		wait, ok := p.Retry(n, time.Now())
		if !ok {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(wait):
		}
	}
}
