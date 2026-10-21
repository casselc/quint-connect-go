// Package retrystorm checks a retry library two ways: the retry budget's
// accounting against a Quint model, and each retry policy's effect on a
// loaded server, where retries can turn a transient overload into a
// metastable failure (qload simulates it, qmeta predicts it, Hegel finds
// the smallest trigger). See README.md.
package retrystorm

import (
	"context"
	"math/rand/v2"
	"time"

	"github.com/casselc/quint-connect-go/examples/retrystorm/retry"
	"github.com/casselc/quint-connect-go/qload"
	"github.com/casselc/quint-connect-go/qmeta"
)

// The system of the papers' running example: one server at 10 requests/s,
// a 100-request queue, clients that time out after 9 s and retry up to 3
// times.
const (
	ServiceRate = 10
	QueueCap    = 100
	Timeout     = 9 * time.Second
	MaxRetries  = 3
	// Backoff's base: retry n waits up to BackoffBase * 2^(n-1).
	BackoffBase = 4 * time.Second
	// The budget: a retry per 10 requests, at most 10 kept.
	BudgetRatio = 100
	BudgetMax   = 10 * retry.Unit
)

// A Policy is one of the retry policies compared.
type Policy struct {
	Name    string
	Backoff bool
	Budget  bool
}

// Policies are the designs compared: retrying at once, with backoff, with
// a budget, and with both.
var Policies = []Policy{
	{Name: "immediate"},
	{Name: "backoff", Backoff: true},
	{Name: "budget", Budget: true},
	{Name: "budget+backoff", Backoff: true, Budget: true},
}

// New makes the library's policy for a run.
func (p Policy) New(rnd *rand.Rand, m retry.Mutation) *retry.Policy {
	rp := &retry.Policy{MaxRetries: MaxRetries}
	if p.Backoff {
		rp.Backoff = retry.Exponential(BackoffBase, time.Minute, rnd)
	}
	if p.Budget {
		rp.Budget = &retry.Budget{Ratio: BudgetRatio, Max: BudgetMax, Mutation: m}
	}
	return rp
}

// Config is the simulated system with clients using the policy at rate
// lambda.
func (p Policy) Config(lambda float64) qload.Config { return p.ConfigWith(lambda, retry.Faithful) }

// ConfigWith is Config with a mutated budget.
func (p Policy) ConfigWith(lambda float64, m retry.Mutation) qload.Config {
	return qload.Config{
		Server: qload.ServerConfig{Name: "server", ServiceRate: ServiceRate, QueueCap: QueueCap},
		Client: qload.ClientConfig{Name: "client", Rate: qload.Constant(lambda), Timeout: Timeout, Retries: MaxRetries,
			Retrier: func(rnd *rand.Rand) qload.Retrier { return p.New(rnd, m) }},
	}
}

// Params is the policy as the CTMC sees it: backoff as its mean wait,
// the budget as a cap on the retry rate.
func (p Policy) Params(lambda float64) qmeta.Params {
	q := qmeta.Params{Lambda: lambda, Mu: ServiceRate, Timeout: Timeout.Seconds(), Retries: MaxRetries, Queue: QueueCap, Orbit: 120}
	if p.Backoff {
		// full jitter: retry n waits base*2^(n-1)/2 on average
		mean := 0.0
		for n := 1; n <= MaxRetries; n++ {
			mean += BackoffBase.Seconds() * float64(int(1)<<(n-1)) / 2 / MaxRetries
		}
		q.Backoff = mean
	}
	if p.Budget {
		q.RetryBudget = float64(BudgetRatio) / retry.Unit
	}
	return q
}

// RecoveryTime is the chain's expected time to drain (10 requests or
// fewer in the system) from a full queue with 30 requests in the orbit.
func RecoveryTime(p Policy, lambda float64) float64 {
	c := qmeta.New(p.Params(lambda))
	h := c.HittingTimes(func(s qmeta.State) bool { return s.U <= 10 })
	return h[c.Index(qmeta.State{U: QueueCap, V: 30})]
}

// Spike is the trigger of the simulation tier: 5 requests/s more for a
// minute.
var Spike = qmeta.Trigger{ExtraRate: 5, Duration: time.Minute}

// Recoveries counts the seeds (1..n) whose run recovers from the trigger
// within 5 minutes.
func Recoveries(p Policy, lambda float64, tr qmeta.Trigger, n int) int {
	ok := 0
	for seed := 1; seed <= n; seed++ {
		cfg := p.Config(lambda)
		cfg.Seed = uint64(seed)
		if qmeta.Recovers(cfg, lambda, tr, 5*time.Minute).Recovered {
			ok++
		}
	}
	return ok
}

// Burst is a scenario a client might see: a few requests and a burst of
// failures, then a long quiet period and another burst.
func Burst(b *retry.Budget) {
	ctx := context.Background()
	for i := 0; i < 30; i++ {
		b.Deposit(ctx)
	}
	for i := 0; i < 5; i++ {
		b.Withdraw(ctx) // three admitted, two refused
	}
	for i := 0; i < 150; i++ {
		b.Deposit(ctx) // fills the bucket
	}
	for i := 0; i < 12; i++ {
		b.Withdraw(ctx)
	}
}
