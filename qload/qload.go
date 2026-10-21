// Package qload simulates request-response load in a qenv simulation:
// open-loop clients with timeouts and retries, servers with a bounded queue
// and a pool of workers, and the time series that show whether a system
// recovers after a stressor or stays stuck (a metastable failure: Isaacs
// et al., "Analyzing Metastable Failures", HotOS '25; Alvaro et al.,
// "Formal Analysis of Metastable Failures in Software Systems", 2025).
//
// The components follow those papers' simulator. A server keeps working on
// a request whose client has given up, which is the wasted work that
// sustains a retry storm. A client retries a request that timed out, up to
// a limit, possibly after a backoff and within a retry budget.
//
// Per-request randomness (arrivals, service times) comes from a seeded
// generator, not the Chooser: a run has tens of thousands of requests.
// What a search should vary (the stressor) is a parameter, which Hegel can
// draw and shrink (see quinthegel and qmeta).
package qload

import (
	"context"
	"math"
	"math/rand/v2"
	"sort"
	"time"

	"github.com/casselc/quint-connect-go/qenv"
)

// Shape is a load shape: a rate (requests per second) or a factor, as a
// function of the elapsed virtual time.
type Shape func(t time.Duration) float64

// Constant is a constant shape.
func Constant(v float64) Shape { return func(time.Duration) float64 { return v } }

// Spike is base, except during [from, to), where it is peak: the transient
// stressor of a metastability experiment.
func Spike(base, peak float64, from, to time.Duration) Shape {
	return func(t time.Duration) float64 {
		if t >= from && t < to {
			return peak
		}
		return base
	}
}

// ServerConfig configures a server.
type ServerConfig struct {
	Name     string
	Workers  int // default 1
	QueueCap int // waiting requests beyond the ones in service; arrivals over it are rejected (default 100)
	// ServiceRate is the rate (per second) of the exponential service time
	// of one worker. Service, if set, samples service times instead.
	ServiceRate float64
	Service     func(r *rand.Rand) time.Duration
	// Slowdown multiplies service times as a function of time (nil: 1): a
	// capacity loss, the other kind of trigger.
	Slowdown Shape
}

// Server is a request-response server in a simulation.
type Server struct {
	cfg   ServerConfig
	sim   *qenv.Sim
	rnd   *rand.Rand
	queue []*job
	busy  int
	work  qenv.Signal
	m     *Metrics
}

type job struct {
	done bool
	sig  qenv.Signal
}

// NewServer starts a server's workers in s.
func NewServer(s *qenv.Sim, rnd *rand.Rand, m *Metrics, cfg ServerConfig) *Server {
	if cfg.Workers == 0 {
		cfg.Workers = 1
	}
	if cfg.QueueCap == 0 {
		cfg.QueueCap = 100
	}
	sv := &Server{cfg: cfg, sim: s, rnd: rnd, m: m}
	for i := 0; i < cfg.Workers; i++ {
		s.Spawn(cfg.Name+".worker", sv.worker)
	}
	return sv
}

func (sv *Server) serviceTime() time.Duration {
	var d time.Duration
	if sv.cfg.Service != nil {
		d = sv.cfg.Service(sv.rnd)
	} else {
		d = time.Duration(sv.rnd.ExpFloat64() / sv.cfg.ServiceRate * float64(time.Second))
	}
	if sv.cfg.Slowdown != nil {
		d = time.Duration(float64(d) * sv.cfg.Slowdown(sv.sim.Elapsed()))
	}
	return d
}

func (sv *Server) worker(ctx context.Context) {
	for {
		for len(sv.queue) == 0 {
			sv.work.Wait(ctx, time.Time{})
		}
		j := sv.queue[0]
		sv.queue = sv.queue[1:]
		sv.busy++
		qenv.Sleep(ctx, sv.serviceTime())
		sv.busy--
		j.done = true
		sv.m.completions++
		j.sig.Notify()
	}
}

// submit enqueues a request, or rejects it (nil) when the queue is full.
func (sv *Server) submit() *job {
	if len(sv.queue) >= sv.cfg.QueueCap {
		sv.m.rejected++
		return nil
	}
	j := &job{}
	sv.queue = append(sv.queue, j)
	sv.work.Notify()
	return j
}

// InSystem is the number of requests queued or in service.
func (sv *Server) InSystem() int { return len(sv.queue) + sv.busy }

// ClientConfig configures an open-loop client.
type ClientConfig struct {
	Name string
	// Rate of new requests (per second) over time: a Poisson process.
	Rate    Shape
	Timeout time.Duration
	// Retries after the first attempt (0: none).
	Retries int
	// Backoff before retry n (1-based); nil retries at once.
	Backoff func(n int, r *rand.Rand) time.Duration
	// RetryBudget, if positive, allows at most this many retries per new
	// request, over a window (a token bucket: each new request adds
	// RetryBudget tokens, each retry takes one, at most 10 kept): the
	// mitigation that bounds a retry storm's amplification.
	RetryBudget float64
}

// Client generates requests to a server.
type Client struct {
	cfg    ClientConfig
	sim    *qenv.Sim
	rnd    *rand.Rand
	sv     *Server
	m      *Metrics
	tokens float64
}

// NewClient starts a client in s.
func NewClient(s *qenv.Sim, rnd *rand.Rand, m *Metrics, sv *Server, cfg ClientConfig) *Client {
	c := &Client{cfg: cfg, sim: s, rnd: rnd, sv: sv, m: m}
	s.Spawn(cfg.Name+".arrivals", c.arrivals)
	return c
}

func (c *Client) arrivals(ctx context.Context) {
	for {
		rate := c.cfg.Rate(c.sim.Elapsed())
		if rate <= 0 {
			qenv.Sleep(ctx, 100*time.Millisecond)
			continue
		}
		qenv.Sleep(ctx, time.Duration(c.rnd.ExpFloat64()/rate*float64(time.Second)))
		c.m.arrivals++
		if c.cfg.RetryBudget > 0 {
			c.tokens = math.Min(10, c.tokens+c.cfg.RetryBudget)
		}
		qenv.Go(ctx, c.cfg.Name+".request", c.request)
	}
}

func (c *Client) request(ctx context.Context) { c.requestFrom(ctx, 0) }

// requestFrom runs a request from its attempt-th attempt (a request
// already in orbit starts at 1).
func (c *Client) requestFrom(ctx context.Context, first int) {
	start := c.sim.Now()
	for attempt := first; ; attempt++ {
		if attempt > 0 {
			c.m.orbit--
		}
		j := c.sv.submit()
		if j != nil && (j.done || j.sig.Wait(ctx, c.sim.Now().Add(c.cfg.Timeout))) {
			c.m.successes++
			c.m.latencies = append(c.m.latencies, c.sim.Now().Sub(start))
			return
		}
		if j != nil {
			c.m.timeouts++
		}
		if attempt >= c.cfg.Retries {
			c.m.failed++
			return
		}
		if c.cfg.RetryBudget > 0 {
			if c.tokens < 1 {
				c.m.failed++
				return
			}
			c.tokens--
		}
		c.m.orbit++
		if c.cfg.Backoff != nil {
			qenv.Sleep(ctx, c.cfg.Backoff(attempt+1, c.rnd))
		}
		c.m.retries++
	}
}

// Sample is one tick of a run's time series.
type Sample struct {
	T time.Duration
	// Queue is the server's requests in system (queued or in service);
	// Orbit the requests waiting to be retried (the papers' orbit).
	Queue, Orbit int
	// Counts over the tick.
	Arrivals, Retries, Successes, Timeouts, Rejected, Failed, Completions int
	// P99 latency of the tick's successes (first attempt to success).
	P99 time.Duration
}

// Metrics accumulates a run's counters and samples them.
type Metrics struct {
	Samples []Sample

	arrivals, retries, successes, timeouts, rejected, failed, completions int
	orbit                                                                 int
	latencies                                                             []time.Duration
}

// sample records the tick ending now and resets the tick's counters.
func (m *Metrics) sample(t time.Duration, sv *Server) {
	s := Sample{T: t, Queue: sv.InSystem(), Orbit: m.orbit, Arrivals: m.arrivals, Retries: m.retries,
		Successes: m.successes, Timeouts: m.timeouts, Rejected: m.rejected, Failed: m.failed, Completions: m.completions}
	if n := len(m.latencies); n > 0 {
		sort.Slice(m.latencies, func(i, j int) bool { return m.latencies[i] < m.latencies[j] })
		s.P99 = m.latencies[(n*99)/100]
	}
	m.Samples = append(m.Samples, s)
	m.arrivals, m.retries, m.successes, m.timeouts, m.rejected, m.failed, m.completions = 0, 0, 0, 0, 0, 0, 0
	m.latencies = m.latencies[:0]
}

// Config is a single-server, single-client system: the papers' running
// example.
type Config struct {
	Server  ServerConfig
	Client  ClientConfig
	Horizon time.Duration // how long to run (default 1000 s)
	Tick    time.Duration // sampling period (default 1 s)
	Seed    uint64
	// Initial state: Queue requests already in the server (work whose
	// clients have given up), Orbit requests already waiting to be retried
	// (they retry within the first Timeout). For calibrating a model from
	// chosen states.
	Initial struct{ Queue, Orbit int }
}

// prefill puts n requests nobody waits for in the server's queue.
func (sv *Server) prefill(n int) {
	for i := 0; i < n && len(sv.queue) < sv.cfg.QueueCap; i++ {
		sv.queue = append(sv.queue, &job{})
	}
	sv.work.Notify()
}

// inject starts n requests in orbit: each retries at a uniform time within
// the timeout, as if its last attempt had timed out.
func (c *Client) inject(n int) {
	for i := 0; i < n; i++ {
		delay := time.Duration(c.rnd.Float64() * float64(c.cfg.Timeout))
		c.m.orbit++
		c.sim.Spawn(c.cfg.Name+".orbit", func(ctx context.Context) {
			qenv.Sleep(ctx, delay)
			c.requestFrom(ctx, 1)
		})
	}
}

// Simulate runs a system for its horizon and returns its time series.
func Simulate(cfg Config) *Metrics {
	if cfg.Horizon == 0 {
		cfg.Horizon = 1000 * time.Second
	}
	if cfg.Tick == 0 {
		cfg.Tick = time.Second
	}
	s := qenv.New(qenv.Nominal{})
	defer s.Close()
	s.MaxSteps = math.MaxInt
	rnd := rand.New(rand.NewPCG(cfg.Seed, 0x51ab1e))
	m := &Metrics{}
	sv := NewServer(s, rnd, m, cfg.Server)
	c := NewClient(s, rnd, m, sv, cfg.Client)
	sv.prefill(cfg.Initial.Queue)
	c.inject(cfg.Initial.Orbit)
	s.Spawn("sampler", func(ctx context.Context) {
		for {
			qenv.Sleep(ctx, cfg.Tick)
			m.sample(s.Elapsed(), sv)
		}
	})
	_ = s.Run(cfg.Horizon)
	return m
}

// Mean of a sample field over the samples with T in [from, to).
func (m *Metrics) Mean(from, to time.Duration, f func(Sample) float64) float64 {
	sum, n := 0.0, 0
	for _, s := range m.Samples {
		if s.T >= from && s.T < to {
			sum += f(s)
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}
