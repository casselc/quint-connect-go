package checkout

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"time"

	"github.com/casselc/quint-connect-go/examples/checkout/shop"
	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qmeta"
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
	// Faults during the run (zero: none, a nominal schedule).
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

// LoadSample is one second of a load run.
type LoadSample struct {
	T time.Duration
	// Model steps that took effect in the second.
	Placed, Charged, Paid, Shipped int
	// Calls the provider served, and its queue (waiting or in service).
	Captures, Provider int
	// Payment events waiting or being processed (the broker's backlog).
	Backlog int
}

// LoadRun is a run's time series and its recovery from the trigger.
type LoadRun struct {
	Samples  []LoadSample
	Recovery qmeta.Recovery
	// Overpaid counts orders charged more than once (none with idempotency
	// keys, whatever the load).
	Overpaid int
}

// counter counts model steps per action, and passes them on.
type counter struct {
	n    map[string]int
	next qobs.Sink
	// charges per order, for Overpaid
	charges map[string]int
}

func (c *counter) Record(ctx context.Context, s *qtrace.Step) {
	c.n[s.Action]++
	if s.Action == "charge" {
		c.charges[fmt.Sprint(s.Args["order"])]++
	}
	if c.next != nil {
		c.next.Record(ctx, s)
	}
}

// Load runs the shop through tr (a load spike and/or a provider slowdown)
// and judges recovery: paid orders per second, over the 100 s before the
// trigger against the 100 s after `within` past its end.
func Load(cfg LoadConfig, tr qmeta.Trigger, within time.Duration) LoadRun {
	if tr.Start == 0 {
		tr.Start = 100 * time.Second
	}
	end := tr.Start + tr.Duration
	horizon := end + within + 100*time.Second
	rnd := rand.New(rand.NewPCG(cfg.Seed, 0xc4ec))
	var c qenv.Chooser = qenv.Nominal{}
	if cfg.Faults.Default > 0 || len(cfg.Faults.Kinds) > 0 {
		sc := qenv.NewSeeded(cfg.Seed)
		sc.Rates = cfg.Faults
		c = sc
	}
	s := qenv.New(c)
	defer s.Close()
	s.MaxSteps = math.MaxInt
	cfg.Config.Stock = math.MaxInt32
	cfg.Config.Provider = &qenv.Capacity{Workers: cfg.Workers, Queue: cfg.ProviderQueue, Shed: cfg.Shed,
		Time: func() time.Duration { return time.Duration(rnd.ExpFloat64() / cfg.ProviderRate * float64(time.Second)) },
		Slowdown: func(t time.Duration) float64 {
			if tr.Slowdown > 1 && t >= tr.Start && t < end {
				return tr.Slowdown
			}
			return 1
		}}
	if cfg.Retry != nil {
		cfg.Config.PaymentRetry = cfg.Retry(rnd)
	}
	cnt := &counter{n: map[string]int{}, charges: map[string]int{}, next: cfg.Sink}
	prev := qobs.Global()
	qobs.SetGlobal(qobs.NewRecorder(cnt, qobs.WithActor("shop")))
	defer qobs.SetGlobal(prev)

	d := Deploy(s, cfg.Config)
	s.Spawn("customers", func(ctx context.Context) {
		for i := 1; ; i++ {
			rate := cfg.Rate
			if t := s.Elapsed(); t >= tr.Start && t < end {
				rate += tr.ExtraRate
			}
			qenv.Sleep(ctx, time.Duration(rnd.ExpFloat64()/rate*float64(time.Second)))
			id := fmt.Sprintf("o%d", i)
			qenv.Go(ctx, "customer", func(ctx context.Context) { d.call(ctx, apiReq{"place", id}) })
		}
	})
	run := LoadRun{}
	captures := 0
	s.Spawn("sampler", func(ctx context.Context) {
		for {
			qenv.Sleep(ctx, time.Second)
			run.Samples = append(run.Samples, LoadSample{T: s.Elapsed(),
				Placed: cnt.n["place"], Charged: cnt.n["charge"], Paid: cnt.n["markPaid"], Shipped: cnt.n["ship"],
				Captures: d.PaySvc.Calls - captures, Provider: d.PaySvc.InSystem(),
				Backlog: d.PayQueue.Pending() + d.PayQueue.InFlight()})
			captures = d.PaySvc.Calls
			clear(cnt.n)
		}
	})
	_ = s.Run(horizon)
	mean := func(from, to time.Duration) float64 {
		sum, n := 0, 0
		for _, x := range run.Samples {
			if x.T >= from && x.T < to {
				sum += x.Paid
				n++
			}
		}
		return float64(sum) / float64(max(n, 1))
	}
	run.Recovery = qmeta.Recovery{Trigger: tr, Before: mean(tr.Start-100*time.Second, tr.Start), After: mean(end+within, horizon)}
	run.Recovery.Judge()
	for _, n := range cnt.charges {
		if n > 1 {
			run.Overpaid++
		}
	}
	return run
}

// System is the experiment as a qmeta.System, for qmeta.Mostly and
// qmeta.Search.
func (cfg LoadConfig) System(within time.Duration) qmeta.System {
	return func(tr qmeta.Trigger, seed uint64) qmeta.Recovery {
		cfg.Seed = seed
		return Load(cfg, tr, within).Recovery
	}
}
