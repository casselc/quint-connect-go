package crossdb

import (
	"math/rand/v2"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/casselc/quint-connect-go/qload"
	"github.com/casselc/quint-connect-go/qmeta"
)

// ControlPlane is the provider's load on the cloud API, abstracted for
// qload and qmeta: each Database is a user of a closed loop (qload's
// Population), observed a poll interval after its last reconcile finished,
// or retried after a requeue delay if it failed. It is the performance
// tier: the correctness of the same provider is checked by crossdb_test.go.
// The abstraction keeps what decides a retry storm (how many reconciles
// are in flight, how soon a failed one comes back, the rate limits) and
// drops the rest (a reconcile is one cloud call, polls are not jittered).
type ControlPlane struct {
	Resources    int           // managed Databases
	PollInterval time.Duration // the managed reconciler's (default 1 minute)

	// The cloud API: Workers serving Rate calls/s each, with a queue of
	// Queue calls; the provider's client gives up on a call after Timeout.
	Workers int
	Rate    float64
	Queue   int
	Timeout time.Duration

	Requeue Requeue
}

// Requeue is how a reconcile that failed is retried.
type Requeue struct {
	Name string
	// Per-item exponential backoff, Base*2^(n-1) up to Max (crossplane-
	// runtime's ratelimiter.NewController: 1s to 60s). Fixed, if set, is a
	// constant delay instead.
	Base, Max, Fixed time.Duration
	// GlobalRate is the provider's reconcile rate limit, shared by every
	// reconcile, polls included (ratelimiter.NewGlobal, the provider's
	// --max-reconcile-rate: 10/s with a burst of 100). 0: none.
	GlobalRate int
}

// The requeue policies compared.
var (
	// CrossplaneDefaults is crossplane-runtime's: per-item backoff from 1s
	// to 60s and a global limit of 10 reconciles/s.
	CrossplaneDefaults = Requeue{Name: "crossplane defaults", Base: time.Second, Max: time.Minute, GlobalRate: 10}
	// NoGlobalLimit raises --max-reconcile-rate out of the way.
	NoGlobalLimit = Requeue{Name: "no global limit", Base: time.Second, Max: time.Minute}
	// FixedRequeue is a hand-written controller that requeues a failed item
	// after a second (reconcile.Result{RequeueAfter: time.Second}, nil),
	// bypassing the workqueue's per-item backoff.
	FixedRequeue = Requeue{Name: "fixed 1s requeue", Fixed: time.Second}
	// FixedRequeueLimited is the same controller under crossplane-runtime's
	// global limit.
	FixedRequeueLimited = Requeue{Name: "fixed 1s requeue, global limit", Fixed: time.Second, GlobalRate: 10}
)

// Requeues are the policies compared.
var Requeues = []Requeue{CrossplaneDefaults, NoGlobalLimit, FixedRequeue, FixedRequeueLimited}

// DefaultControlPlane is a provider managing 600 Databases against a cloud
// API that serves 20 calls/s (4 workers at 5/s each). The provider times
// out after 2 s, as the simulation's dbaas client does.
func DefaultControlPlane(r Requeue) ControlPlane {
	return ControlPlane{Resources: 600, PollInterval: time.Minute, Workers: 4, Rate: 5, Queue: 100, Timeout: 2 * time.Second, Requeue: r}
}

// retries bounds a request's retries: a controller retries forever, and
// within a run's horizon that is this many times.
const retries = 50

// Lambda is the nominal observe rate: Resources per PollInterval.
func (c ControlPlane) Lambda() float64 { return float64(c.Resources) / c.PollInterval.Seconds() }

// Config is the control plane as a qload system.
func (c ControlPlane) Config() qload.Config {
	return qload.Config{
		Server: qload.ServerConfig{Name: "dbaas", Workers: c.Workers, ServiceRate: c.Rate, QueueCap: c.Queue},
		Client: qload.ClientConfig{Name: "provider", Population: c.Resources, Think: c.PollInterval, Timeout: c.Timeout, Retries: retries,
			Retrier: func(*rand.Rand) qload.Retrier { return newRequeuer(c.Requeue) }},
	}
}

// requeuer is the workqueue's rate limiting on virtual time: the global
// token bucket (x/time/rate takes the time as an argument) and the per-item
// exponential delay, the larger of the two (workqueue's MaxOf).
type requeuer struct {
	r      Requeue
	mu     sync.Mutex
	global *rate.Limiter
}

func newRequeuer(r Requeue) *requeuer {
	q := &requeuer{r: r}
	if r.GlobalRate > 0 {
		q.global = rate.NewLimiter(rate.Limit(r.GlobalRate), 10*r.GlobalRate)
	}
	return q
}

// Request takes a poll's token: polls are reconciles too. A poll is not
// delayed here, but it leaves fewer tokens for retries.
func (q *requeuer) Request(now time.Time) {
	if q.global != nil {
		q.mu.Lock()
		q.global.ReserveN(now, 1)
		q.mu.Unlock()
	}
}

func (q *requeuer) Retry(n int, now time.Time) (time.Duration, bool) {
	d := q.r.Fixed
	if d == 0 {
		d = q.r.Base << (n - 1)
		if d > q.r.Max || d <= 0 {
			d = q.r.Max
		}
	}
	if q.global != nil {
		q.mu.Lock()
		d = max(d, q.global.ReserveN(now, 1).DelayFrom(now))
		q.mu.Unlock()
	}
	return d, true
}

// Params is the control plane as the CTMC sees it: open-loop polls at
// Lambda, backoff as the mean delay of a retry, the global limit as a cap
// on the retry rate (what the polls leave of it). The chain has no closed
// loop: it counts a failing object's polls on top of its retries, so it is
// pessimistic in a storm.
func (c ControlPlane) Params() qmeta.Params {
	p := qmeta.Params{Lambda: c.Lambda(), Mu: c.Rate, Workers: c.Workers, Timeout: c.Timeout.Seconds(), Retries: retries, Queue: c.Queue, Orbit: 2 * c.Queue}
	if c.Requeue.Fixed > 0 {
		p.Backoff = c.Requeue.Fixed.Seconds()
	} else {
		// a storm's retries have mostly reached the cap
		p.Backoff = c.Requeue.Max.Seconds()
	}
	if c.Requeue.GlobalRate > 0 {
		p.RetryRate = max(float64(c.Requeue.GlobalRate)-c.Lambda(), 0.1)
	}
	return p
}
