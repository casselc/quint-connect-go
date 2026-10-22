package checkout_test

import (
	"math/rand/v2"
	"testing"
	"time"

	"github.com/casselc/quint-connect-go/examples/checkout"
	"github.com/casselc/quint-connect-go/examples/checkout/shop"
	"github.com/casselc/quint-connect-go/examples/retrystorm/retry"
	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qload"
	"github.com/casselc/quint-connect-go/qobs"
	"github.com/casselc/quint-connect-go/qtest"
)

// dip is the trigger: the provider serves three times slower for a minute.
var dip = qload.Trigger{Slowdown: 3, Duration: time.Minute}

func recoveries(cfg checkout.LoadConfig, tr qload.Trigger, n int) int {
	return qload.Count(cfg.System(5*time.Minute), tr, n)
}

func consumers(n int) checkout.LoadConfig {
	cfg := checkout.DefaultLoad()
	cfg.Rate, cfg.PaymentWorkers = 3, n
	return cfg
}

// 3 orders/s against a provider that captures 5/s, with a 1 s timeout.
// With 4 payment consumers the shop never collapses; with 6 it collapses
// on its own, without any trigger: once a few captures time out, their
// redeliveries keep every consumer busy, 6 calls queue at the provider
// (1.2 s of work against a 1 s timeout), they all time out, and the
// provider keeps working on calls nobody waits for. The bound is the
// provider's capacity times the timeout: 5 consumers.
func TestConsumersBeyondCapacityCollapse(t *testing.T) {
	for _, c := range []struct{ consumers, maxBacklog, minBacklog int }{{4, 100, 0}, {6, 1 << 30, 200}} {
		for i, r := range qload.Runs(consumers(c.consumers).System(5*time.Minute), qload.Trigger{}, 2) {
			backlog := r.Series.Last("backlog")
			t.Logf("%d consumers, seed %d: %d payment events in the backlog after %v", c.consumers, i+1, backlog, r.Series.Points[len(r.Series.Points)-1].T)
			if backlog > c.maxBacklog || backlog < c.minBacklog {
				t.Errorf("%d consumers: backlog %d; want %d to %d", c.consumers, backlog, c.minBacklog, c.maxBacklog)
			}
		}
	}
}

// The mitigations, with 8 consumers through a provider slowdown:
//   - a retry budget in the shop does not help: a capture it refuses is
//     returned to the broker, which redelivers it at once;
//   - delaying redelivery only slows the storm;
//   - the provider dropping calls whose caller has given up (deadline
//     propagation) removes the wasted work, and the shop recovers;
//   - so does bounding the consumers by the provider's capacity.
func TestMitigations(t *testing.T) {
	if testing.Short() {
		t.Skip("15 load runs of 8 simulated minutes")
	}
	budget := func(*rand.Rand) shop.Retrier {
		return &retry.Policy{MaxRetries: 3, Backoff: func(int) time.Duration { return 200 * time.Millisecond },
			Budget: &retry.Budget{Ratio: 100, Max: 10 * retry.Unit}}
	}
	cases := []struct {
		name            string
		cfg             func(*checkout.LoadConfig)
		atLeast, atMost int
	}{
		{"8 consumers", func(*checkout.LoadConfig) {}, 0, 0},
		{"retry budget", func(c *checkout.LoadConfig) { c.Retry = budget }, 0, 0},
		{"redelivery after 5 s", func(c *checkout.LoadConfig) { c.RedeliveryDelay = 5 * time.Second }, 0, 2},
		{"provider sheds expired calls", func(c *checkout.LoadConfig) { c.Shed = true }, 3, 3},
		{"4 consumers", func(c *checkout.LoadConfig) { c.PaymentWorkers = 4 }, 3, 3},
	}
	for _, c := range cases {
		cfg := consumers(8)
		c.cfg(&cfg)
		got := recoveries(cfg, dip, 3)
		t.Logf("%-30s %d/3 runs recover after %v", c.name, got, dip)
		if got < c.atLeast || got > c.atMost {
			t.Errorf("%s: %d of 3 runs recover; want %d to %d", c.name, got, c.atLeast, c.atMost)
		}
	}
}

// loadStock checks a load run against the instance of the model with its
// stock (checkout.LoadConfig stocks math.MaxInt32 units).
var loadStock = qtest.Constants(map[string]string{"STOCK": "2147483647"})

// stormRun records a collapsing run: 8 consumers, a dip at 30 s.
func stormRun(t *testing.T, cfg checkout.LoadConfig) (checkout.LoadRun, *qobs.MemorySink) {
	mem := &qobs.MemorySink{}
	cfg.Sink, cfg.Seed = mem, 1
	r := checkout.Load(cfg, qload.Trigger{Start: 30 * time.Second, Slowdown: 3, Duration: 30 * time.Second}, time.Minute)
	t.Logf("%d steps; goodput %.1f/s before, %.1f/s after; %d orders charged twice",
		len(mem.Steps()), r.Recovery.Before, r.Recovery.After, r.Overpaid)
	return r, mem
}

// Through the storm, with crashes, lost responses and redeliveries on
// top, the faithful shop stays correct: every capture is retried many
// times, and the idempotency key keeps each order charged once.
func TestStormIsCorrect(t *testing.T) {
	qtest.RequireQuint(t)
	if testing.Short() {
		t.Skip("checks a run of ~1300 steps")
	}
	cfg := consumers(8)
	cfg.NoCrashes = false
	// a crash every 20 s on average; one lost response or ack in a hundred
	cfg.Faults = qenv.Rates{Kinds: map[string]float64{"fault.nemesis": 0.05, "fault.rpc": 0.01, "fault.queue": 0.01}}
	_, mem := stormRun(t, cfg)
	qtest.Validate(t, binding, mem.Steps(), loadStock).Expect(t, qtest.Conforms())
}

// Without idempotency keys, the storm alone, with no injected fault,
// charges orders twice: a timeout is as ambiguous as a lost response, and
// a storm is thousands of timeouts.
func TestStormChargesTwiceWithoutKeys(t *testing.T) {
	qtest.RequireQuint(t)
	if testing.Short() {
		t.Skip("checks a run of ~1700 steps")
	}
	cfg := consumers(8)
	cfg.Mutation = shop.NoIdempotencyKey
	r, mem := stormRun(t, cfg)
	if r.Overpaid == 0 {
		t.Error("no order charged twice")
	}
	qtest.Validate(t, binding, mem.Steps(), loadStock).Expect(t, qtest.Violates("chargedAtMostOnce"))
}
