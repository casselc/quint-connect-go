package sim

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/casselc/quint-connect-go/examples/edgepublish/publisher"
	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qload"
	"github.com/casselc/quint-connect-go/qobs"
	"github.com/casselc/quint-connect-go/qtrace"
)

// LoadConfig is the publisher under load: requests reach the collector at
// Rate per second and Workers exporters push them to an S3 of limited
// capacity. A write S3 does not answer within Timeout is ambiguous, as with
// the real S3: the request may still be served (and is: qenv.Capacity serves
// calls whose caller gave up), so the write may have landed.
type LoadConfig struct {
	Rate     float64 // requests per second (default 2)
	Workers  int     // concurrent exports (default 4)
	S3       int     // S3 workers (default 2), each serving S3Rate writes/s (default 5)
	S3Rate   float64
	S3Queue  int           // default 100
	Timeout  time.Duration // an S3 write's (default 2 s)
	Retry    time.Duration // the collector's pause before retrying a failed export (default 5 s)
	Mutation publisher.Mutation
}

func (c *LoadConfig) defaults() {
	if c.Rate == 0 {
		c.Rate = 2
	}
	if c.Workers == 0 {
		c.Workers = 4
	}
	if c.S3 == 0 {
		c.S3 = 2
	}
	if c.S3Rate == 0 {
		c.S3Rate = 5
	}
	if c.S3Queue == 0 {
		c.S3Queue = 100
	}
	if c.Timeout == 0 {
		c.Timeout = 2 * time.Second
	}
	if c.Retry == 0 {
		c.Retry = 5 * time.Second
	}
}

// Goodput is the step whose rate is the exporter's goodput: a manifest
// that committed its batch.
const Goodput = "writeManifest/ok"

// Commits counts, per request, the batches that committed it (a manifest
// write that landed: ok, or ambiguous). More than one is a duplicate
// (the model's committedNoDuplicatePayload, finding F3).
type Commits struct {
	mu   sync.Mutex
	n    map[string]int
	next qobs.Sink
}

// NewCommits passes steps on to next (nil: none).
func NewCommits(next qobs.Sink) *Commits { return &Commits{n: map[string]int{}, next: next} }

// Record implements qobs.Sink.
func (c *Commits) Record(ctx context.Context, s *qtrace.Step) {
	if s.Action == "writeManifest" && (s.Outcome == "ok" || s.Outcome == "ambiguous") {
		c.mu.Lock()
		c.n[fmt.Sprint(s.Args["payload"])]++
		c.mu.Unlock()
	}
	if c.next != nil {
		c.next.Record(ctx, s)
	}
}

// Duplicates is the number of requests committed more than once.
func (c *Commits) Duplicates() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	d := 0
	for _, n := range c.n {
		if n > 1 {
			d++
		}
	}
	return d
}

// Experiment is the publisher as a qload experiment; its steps go to sink.
func (cfg LoadConfig) Experiment(sink qobs.Sink) qload.Experiment {
	cfg.defaults()
	return qload.Experiment{Rate: cfg.Rate, Healthy: cfg.Rate, Sink: sink, Actor: "edge-1/traces",
		Deploy: func(env *qload.Env) func(ctx context.Context, i int) {
			rnd := env.Rand("s3.service")
			s3 := qenv.NewService(env.Sim, "s3", nil, func(context.Context, publisher.Op) (bool, error) { return true, nil })
			s3.Capacity = &qenv.Capacity{Workers: cfg.S3, Queue: cfg.S3Queue, Slowdown: env.Trigger.Factor(),
				Time: func() time.Duration { return time.Duration(rnd.ExpFloat64() / cfg.S3Rate * float64(time.Second)) }}
			store := publisher.NewMemStore()
			store.Hook = func(ctx context.Context, op publisher.Op) publisher.Fault {
				_, err := s3.Call(ctx, op, cfg.Timeout)
				switch {
				case errors.Is(err, qenv.ErrTimeout):
					return publisher.Ambiguous
				case err != nil:
					return publisher.Fail // rejected: S3's queue was full
				}
				return publisher.Apply
			}
			e := &exporter{s: env.Sim, cfg: Config{Workers: cfg.Workers, NoCrashes: true, Mutation: cfg.Mutation},
				store: store, exported: map[string]bool{}, retry: cfg.Retry}
			e.start()
			env.Probe("backlog", e.Backlog)
			env.Probe("s3", s3.InSystem)
			return func(_ context.Context, i int) { e.offer(fmt.Sprintf("req-%d", i)) }
		}}
}
