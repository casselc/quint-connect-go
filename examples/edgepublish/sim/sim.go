// Package sim runs the publisher, unchanged, in a qenv simulation. Built
// with Orchestrion (`orchestrion go test ./sim/`), qenv/weave rewrites the
// publisher's sync.RWMutex, sync.WaitGroup and its //qenv:go goroutine,
// so its pushes, rotations and background seals become simulation tasks
// whose interleaving Hegel or a seed chooses. Its //quint:action steps
// record into the run.
//
// The environment:
//   - S3 and the table store (publisher.MemStore), whose writes may fail
//     or fail ambiguously (applied, then an error: "fault.s3.<kind>") and
//     take a chosen time ("sched.s3.latency"), during which the clock
//     moves and other pushes run;
//   - the collector's persistent queue: requests arrive over time, and a
//     request whose export failed is retried, also by the next incarnation;
//   - the exporter as a crashable process: a crash abandons its in-flight
//     pushes and background seals, and the next incarnation is a new epoch.
package sim

import (
	"context"
	"fmt"
	"time"

	"github.com/casselc/quint-connect-go/examples/edgepublish/publisher"
	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qobs"
)

// Config of a simulated run.
type Config struct {
	Mutation publisher.Mutation
	Requests int // collector requests over the run (default 6)
	Workers  int // concurrent exports (default 2)
	// NoCrashes keeps the exporter up; NoAmbiguous makes failed writes
	// plain failures (no write lands and reports an error).
	NoCrashes, NoAmbiguous bool
}

// Generation is the publisher's generation length in the simulation.
const Generation = time.Minute

// exporter is the collector's queue and the exporter process: requests
// arrive (offer), workers export them with retries, a ticker rotates, and
// the process may crash. The queue outlives the exporter's incarnations.
type exporter struct {
	s        *qenv.Sim
	cfg      Config
	store    publisher.Store
	queue    []string
	exported map[string]bool
	current  *publisher.Publisher
	proc     *qenv.Process
	// Retry is the pause before a failed export is retried (default 5 s).
	retry time.Duration
	// stopTicking ends the ticker (exploration: once every request is in)
	stopTicking func() bool
}

func (e *exporter) offer(id string) { e.queue = append(e.queue, id) }

// Backlog is the number of requests not yet exported.
func (e *exporter) Backlog() int { return len(e.queue) - len(e.exported) }

func (e *exporter) start() {
	if e.retry == 0 {
		e.retry = 5 * time.Second
	}
	s, cfg := e.s, e.cfg
	e.proc = s.Process("exporter", !cfg.NoCrashes, func(ctx context.Context) {
		inc := qenv.ProcessOf(ctx).Incarnation
		p := publisher.New(publisher.Config{Producer: "edge-1", Region: "eu", Signal: "traces",
			Epoch: fmt.Sprintf("epoch-%d", inc), Generation: Generation, Now: s.Now, Mutation: cfg.Mutation}, e.store)
		e.current = p
		taken := map[string]bool{}
		next := 0 // queue[:next] are taken or exported
		for w := 0; w < cfg.Workers; w++ {
			qenv.Go(ctx, "export", func(ctx context.Context) {
				qenv.Daemon(ctx) // a worker waits for exports forever once the queue is drained
				for {
					id := ""
					qenv.Wait(ctx, func() bool {
						for next < len(e.queue) && (e.exported[e.queue[next]] || taken[e.queue[next]]) {
							next++
						}
						if next < len(e.queue) {
							id = e.queue[next]
							return true
						}
						return false
					})
					taken[id] = true
					// the collector retries a failed export after a pause
					for !e.exported[id] {
						if p.Push(publisher.WithPayload(ctx, id), []byte("rows of "+id)) == nil {
							e.exported[id] = true
						} else {
							qenv.Sleep(ctx, e.retry)
						}
					}
				}
			})
		}
		// rotate when the clock has moved on, as a push would
		for e.stopTicking == nil || !e.stopTicking() {
			qenv.Sleep(ctx, 20*time.Second)
			_ = p.Tick(ctx)
		}
	})
}

// Run runs the exporter for 3 minutes of virtual time with faults, then
// lets the last incarnation drain its queue and close.
func Run(c qenv.Chooser, cfg Config) error { return RunOn(qenv.New(c), cfg) }

// RunOn is Run on a simulation of any runtime: qenv's (woven by
// qenv/weave), or the native one in gosim, where the publisher's locks,
// wait group and goroutine are gosim's (examples/gosim/edgepublish). It
// closes s.
func RunOn(s *qenv.Sim, cfg Config) error {
	if cfg.Requests == 0 {
		cfg.Requests = 6
	}
	if cfg.Workers == 0 {
		cfg.Workers = 2
	}
	defer s.Close()
	store := publisher.NewMemStore()
	store.Hook = func(ctx context.Context, op publisher.Op) publisher.Fault {
		if d := s.Choose("sched.s3.latency", 4); d > 0 {
			qenv.Sleep(ctx, time.Duration(d)*5*time.Second)
		}
		n := 3
		if cfg.NoAmbiguous {
			n = 2
		}
		return publisher.Fault(s.Fault("s3."+op.Kind, n))
	}
	e := &exporter{s: s, cfg: cfg, store: store, exported: map[string]bool{}}
	e.stopTicking = func() bool { return len(e.exported) >= cfg.Requests }
	s.Spawn("collector", func(ctx context.Context) {
		for i := 1; i <= cfg.Requests; i++ {
			qenv.Sleep(ctx, time.Duration(10+10*s.Choose("sched.arrival", 3))*time.Second)
			e.offer(fmt.Sprintf("req-%d", i))
		}
	})
	e.start()
	if err := s.Run(3 * time.Minute); err != nil {
		return err
	}
	// the last incarnation drains the queue, then closes
	if err := s.Settle(5 * time.Minute); err != nil {
		return err
	}
	if e.proc.Down() {
		return nil // crashed during the settle's start: nothing to close
	}
	s.Spawn("close", func(ctx context.Context) { _ = e.current.Close(ctx) })
	return s.Settle(time.Minute)
}

// Woven reports whether the build wove this module (orchestrion go test):
// probe's //quint:action records a step only then. Unwoven, the publisher
// records nothing and its real locks would block the simulation's
// scheduler, so tests skip.
func Woven() bool {
	mem := &qobs.MemorySink{}
	probe(qobs.WithRecorder(context.Background(), qobs.NewRecorder(mem)))
	return len(mem.Steps()) > 0
}

//quint:action action:woven
func probe(ctx context.Context) {}
