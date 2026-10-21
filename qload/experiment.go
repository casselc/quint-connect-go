package qload

import (
	"context"
	"fmt"
	"hash/fnv"
	"math"
	"math/rand/v2"
	"sort"
	"sync"
	"time"

	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qobs"
	"github.com/casselc/quint-connect-go/qtrace"
)

// A Trigger is a transient stressor: for Duration, starting at Start, the
// load is ExtraRate requests per second above the base rate and the
// dependency it is applied to (Factor) is Slowdown times slower.
type Trigger struct {
	Start     time.Duration
	Duration  time.Duration
	ExtraRate float64
	Slowdown  float64
}

func (tr Trigger) String() string {
	s := fmt.Sprintf("+%.1f req/s", tr.ExtraRate)
	if tr.Slowdown > 1 {
		s += fmt.Sprintf(" and %.2gx slower", tr.Slowdown)
	}
	return s + " for " + tr.Duration.String()
}

// End is when the stressor ends.
func (tr Trigger) End() time.Duration { return tr.Start + tr.Duration }

// Active reports whether the stressor is on at t.
func (tr Trigger) Active(t time.Duration) bool { return t >= tr.Start && t < tr.End() }

// Rate is base plus ExtraRate while the stressor is on.
func (tr Trigger) Rate(base float64) Shape { return Spike(base, base+tr.ExtraRate, tr.Start, tr.End()) }

// Factor is Slowdown while the stressor is on, else 1: what a service time
// is multiplied by (qenv.Capacity.Slowdown, ServerConfig.Slowdown).
func (tr Trigger) Factor() Shape {
	s := tr.Slowdown
	if s <= 0 {
		s = 1
	}
	return Spike(1, s, tr.Start, tr.End())
}

// An Experiment puts an application, running in a qenv simulation, under
// load. Customers make requests (Deploy's), open-loop at Rate per second
// or as a closed loop of Population users who think Think between
// requests. Throughput is measured from the application's own model steps:
// the steps it records (through the simulation's contexts) are counted per
// tick, so goodput is the rate of a model action ("markPaid"), and the same
// steps can go to Sink to be checked against the model.
type Experiment struct {
	// Deploy builds the system in env.Sim, with env.Trigger in effect where
	// it applies (a dependency's slowdown: env.Trigger.Factor()), registers
	// its probes (env.Probe), and returns what one customer request does.
	Deploy func(env *Env) func(ctx context.Context, i int)

	Rate float64 // open-loop requests per second (plus the trigger's ExtraRate)
	// Healthy is the goodput (per second) of the system when it is well,
	// if known: for an open loop with one goodput step per request, Rate.
	// A recovery is judged against it, not only against the goodput before
	// the trigger, which a system already collapsed would match (qmeta).
	Healthy    float64
	Population int // closed-loop users (0: none)
	Think      time.Duration

	// Faults during the run (zero: none and a canonical schedule), or a
	// Chooser for the seed (overrides Faults).
	Faults  qenv.Rates
	Chooser func(seed uint64) qenv.Chooser
	// NemesisEvery sets the simulation's (qenv.Sim.NemesisEvery): crashes
	// at a rate per virtual second, not per scheduling step, which under
	// load would mean far more crashes (default 1 s when Faults or Chooser
	// is set; negative: per step).
	NemesisEvery time.Duration
	Tick         time.Duration // sampling period (default 1 s)
	// Sink, if set, also receives the run's steps; Actor names them
	// (default "app").
	Sink  qobs.Sink
	Actor string
}

// Env is what Deploy builds in.
type Env struct {
	Sim     *qenv.Sim
	Trigger Trigger
	seed    uint64
	probes  map[string]func() int
}

// Rand is a generator for one stream of the run's randomness (service
// times, arrivals): seeded from the run's seed and the stream's name, so
// streams are independent and a change to one leaves the others as they
// were. Not the Chooser: a load run has thousands of such draws, and what a
// search varies is the trigger.
func (e *Env) Rand(stream string) *rand.Rand {
	h := fnv.New64a()
	_, _ = h.Write([]byte(stream))
	return rand.New(rand.NewPCG(e.seed, h.Sum64()))
}

// Probe samples a gauge (a queue's length, a backlog) every tick.
func (e *Env) Probe(name string, f func() int) { e.probes[name] = f }

// A Point is one tick of a Series.
type Point struct {
	T      time.Duration
	Steps  map[string]int // model steps recorded in the tick, by action
	Probes map[string]int // gauges at the end of the tick
}

// Series is a run's time series.
type Series struct {
	Tick   time.Duration
	Points []Point
}

// Count is the number of steps of an action recorded in [from, to) (by
// the ticks that end in it).
func (s *Series) Count(action string, from, to time.Duration) int {
	n := 0
	for _, p := range s.Points {
		if p.T > from && p.T <= to {
			n += p.Steps[action]
		}
	}
	return n
}

// Rate is Count per second.
func (s *Series) Rate(action string, from, to time.Duration) float64 {
	return float64(s.Count(action, from, to)) / (to - from).Seconds()
}

// Last is a probe's last sample.
func (s *Series) Last(probe string) int {
	if len(s.Points) == 0 {
		return 0
	}
	return s.Points[len(s.Points)-1].Probes[probe]
}

// counter counts steps by action, and passes them on.
type counter struct {
	mu   sync.Mutex
	n    map[string]int
	next qobs.Sink
}

func (c *counter) Record(ctx context.Context, s *qtrace.Step) {
	c.mu.Lock()
	c.n[s.Action]++
	c.mu.Unlock()
	if c.next != nil {
		c.next.Record(ctx, s)
	}
}

// Run runs the experiment with seed and trigger for horizon of virtual
// time.
func (x Experiment) Run(seed uint64, tr Trigger, horizon time.Duration) *Series {
	if x.Tick == 0 {
		x.Tick = time.Second
	}
	if x.Actor == "" {
		x.Actor = "app"
	}
	var c qenv.Chooser = qenv.Nominal{}
	switch {
	case x.Chooser != nil:
		c = x.Chooser(seed)
	case x.Faults.Default > 0 || len(x.Faults.Kinds) > 0:
		sc := qenv.NewSeeded(seed)
		sc.Rates = x.Faults
		c = sc
	}
	defer qobs.Strict()()
	cnt := &counter{n: map[string]int{}, next: x.Sink}
	s := qenv.New(qenv.WithContext(c, qobs.WithRecorder(context.Background(), qobs.NewRecorder(cnt, qobs.WithActor(x.Actor)))))
	defer s.Close()
	s.MaxSteps = math.MaxInt
	switch {
	case x.NemesisEvery > 0:
		s.NemesisEvery = x.NemesisEvery
	case x.NemesisEvery == 0:
		s.NemesisEvery = time.Second
	}
	env := &Env{Sim: s, Trigger: tr, seed: seed, probes: map[string]func() int{}}
	request := x.Deploy(env)
	arrivals := env.Rand("qload.arrivals")
	if x.Rate > 0 || tr.ExtraRate > 0 {
		rate := tr.Rate(x.Rate)
		s.Spawn("customers", func(ctx context.Context) {
			for i := 1; ; i++ {
				r := rate(s.Elapsed())
				if r <= 0 {
					qenv.Sleep(ctx, x.Tick)
					continue
				}
				qenv.Sleep(ctx, time.Duration(arrivals.ExpFloat64()/r*float64(time.Second)))
				qenv.Go(ctx, "customer", func(ctx context.Context) { request(ctx, i) })
			}
		})
	}
	think := env.Rand("qload.think")
	for u := 0; u < x.Population; u++ {
		s.Spawn("user", func(ctx context.Context) {
			qenv.Sleep(ctx, time.Duration(think.Float64()*float64(x.Think)))
			for i := 0; ; i++ {
				request(ctx, u+i*x.Population)
				qenv.Sleep(ctx, time.Duration(think.ExpFloat64()*float64(x.Think)))
			}
		})
	}
	series := &Series{Tick: x.Tick}
	names := make([]string, 0, len(env.probes))
	for n := range env.probes {
		names = append(names, n)
	}
	sort.Strings(names)
	s.Spawn("qload.sampler", func(ctx context.Context) {
		for {
			qenv.Sleep(ctx, x.Tick)
			p := Point{T: s.Elapsed(), Probes: map[string]int{}}
			cnt.mu.Lock()
			p.Steps, cnt.n = cnt.n, map[string]int{}
			cnt.mu.Unlock()
			for _, n := range names {
				p.Probes[n] = env.probes[n]()
			}
			series.Points = append(series.Points, p)
		}
	})
	_ = s.Run(horizon)
	return series
}
