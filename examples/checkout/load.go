package checkout

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/casselc/quint-connect-go/examples/checkout/shop"
	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qload"
	"github.com/casselc/quint-connect-go/qobs"
	"github.com/casselc/quint-connect-go/qtrace"
)

// LoadConfig is a load experiment on the shop: customers placing orders at
// Rate per second against a payment provider of limited capacity, with
// the shop's real code (saga, outbox, consumers, retries) in between.
type LoadConfig struct {
	Config
	Rate float64 // orders per second (a Poisson process)
	// The provider: Workers serving ProviderRate calls/s each, with a
	// queue of ProviderQueue; Shed drops calls whose caller gave up.
	Workers       int
	ProviderRate  float64
	ProviderQueue int
	Shed          bool
	// Retry makes the payment retry policy for a run (nil: the shop's
	// default, 4 attempts 200 ms apart).
	Retry func(rnd *rand.Rand) shop.Retrier
	// Faults during the run (zero: none, a nominal schedule). A crash
	// ("fault.nemesis") is a choice once per virtual second.
	Faults qenv.Rates
	Seed   uint64
	// Sink, if set, also receives the run's model steps.
	Sink qobs.Sink
}

// DefaultLoad is the experiment of load_test.go: 4 orders/s against a
// provider that captures 5/s (one worker), 4 payment consumers, a 1 s
// call timeout (payments.call).
func DefaultLoad() LoadConfig {
	return LoadConfig{Config: Config{Isolation: qenv.Serializable, NoCrashes: true, PaymentWorkers: 4},
		Rate: 4, Workers: 1, ProviderRate: 5, ProviderQueue: 50}
}

// Goodput is the model step whose rate is the shop's goodput.
const Goodput = "markPaid"

// LoadRun is a run's time series (model steps per second; probes
// "provider", the calls at the provider, and "backlog", the payment events
// waiting or being processed) and its recovery from the trigger.
type LoadRun struct {
	Recovery qload.Recovery
	// Overpaid counts orders charged more than once (none with idempotency
	// keys, whatever the load).
	Overpaid int
}

// charges counts charges per order, and passes steps on.
type charges struct {
	mu   sync.Mutex
	n    map[string]int
	next qobs.Sink
}

func (c *charges) Record(ctx context.Context, s *qtrace.Step) {
	if s.Action == "charge" {
		c.mu.Lock()
		c.n[fmt.Sprint(s.Args["order"])]++
		c.mu.Unlock()
	}
	if c.next != nil {
		c.next.Record(ctx, s)
	}
}

// Experiment is the shop as a qload experiment: Deploy, with the trigger's
// slowdown applied to the provider, and a customer request placing order
// o<i>. sink, if set, receives the model steps.
func (cfg LoadConfig) Experiment(sink qobs.Sink) qload.Experiment {
	return qload.Experiment{
		Rate: cfg.Rate, Healthy: cfg.Rate, Faults: cfg.Faults, Sink: sink, Actor: "shop",
		Deploy: func(env *qload.Env) func(ctx context.Context, i int) {
			c := cfg.Config
			c.Stock = math.MaxInt32
			service := env.Rand("provider.service")
			c.Provider = &qenv.Capacity{Workers: cfg.Workers, Queue: cfg.ProviderQueue, Shed: cfg.Shed, Slowdown: env.Trigger.Factor(),
				Time: func() time.Duration {
					return time.Duration(service.ExpFloat64() / cfg.ProviderRate * float64(time.Second))
				}}
			if cfg.Retry != nil {
				c.PaymentRetry = cfg.Retry(env.Rand("shop.retry"))
			}
			d := Deploy(env.Sim, c)
			env.Probe("provider", d.PaySvc.InSystem)
			env.Probe("backlog", func() int { return d.PayQueue.Pending() + d.PayQueue.InFlight() })
			return func(ctx context.Context, i int) { d.call(ctx, apiReq{"place", fmt.Sprintf("o%d", i)}) }
		},
	}
}

// Load runs the shop through tr (a load spike and/or a provider slowdown)
// and judges recovery by paid orders per second (qload.Assess).
func Load(cfg LoadConfig, tr qload.Trigger, within time.Duration) LoadRun {
	ch := &charges{n: map[string]int{}, next: cfg.Sink}
	run := LoadRun{Recovery: qload.Assess(cfg.Experiment(ch), Goodput, tr, within, cfg.Seed)}
	for _, n := range ch.n {
		if n > 1 {
			run.Overpaid++
		}
	}
	return run
}

// System is the experiment as a qload.Target, for qload.Mostly and
// qmeta.Search.
func (cfg LoadConfig) System(within time.Duration) qload.Target {
	return qload.Of(cfg.Experiment(nil), Goodput, within)
}
