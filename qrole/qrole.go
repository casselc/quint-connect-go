// Package qrole measures the load roles of a running system: the requests a
// server is serving, the operations clients have in progress, and their
// attempts in flight. These are the quantities qmeta's chain is built from
// (DESIGN.md §15.3):
//   - u, the requests at a server (Queue): from arrival to response,
//     waiting for a worker included, and on after the client gave up;
//   - v, the orbit (Orbit): a client's operations in progress with no
//     attempt in flight, that is waiting to retry.
//
// No dashboard has v by default, and it is what tells a slow system from a
// metastable one. Mark the roles in code with the annotations qrole's
// aspects weave (orchestrion.yml: //quint:serve name:N, //quint:operation
// service:S, //quint:call service:S), or call Serve, Operation and Call.
// Sample the gauges in a simulation (qload's probes), or export them with
// OTel (Roles.Instrument) and alert on them with the rules qmeta derives
// from the chain (qmeta.Region.Rules).
package qrole

import (
	"context"
	"sort"
	"sync"
	"sync/atomic"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Roles is a registry of load-role counts.
type Roles struct {
	mu    sync.Mutex
	serve map[string]*stats
	ops   map[string]*stats
	calls map[string]*stats
}

type stats struct {
	inflight atomic.Int64
	ok, err  atomic.Int64 // completed, by outcome
}

// New returns an empty registry.
func New() *Roles {
	return &Roles{serve: map[string]*stats{}, ops: map[string]*stats{}, calls: map[string]*stats{}}
}

// Default is the registry of contexts that carry none (WithRoles).
var Default = New()

type key struct{}

// WithRoles makes ctx record into r: one registry per simulated run, as
// qobs.WithRecorder does for steps.
func WithRoles(ctx context.Context, r *Roles) context.Context {
	return context.WithValue(ctx, key{}, r)
}

// From returns ctx's registry, or Default.
func From(ctx context.Context) *Roles {
	if r, ok := ctx.Value(key{}).(*Roles); ok {
		return r
	}
	return Default
}

func (r *Roles) get(m map[string]*stats, name string) *stats {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := m[name]
	if !ok {
		s = &stats{}
		m[name] = s
	}
	return s
}

func track(s *stats) func(err error) {
	s.inflight.Add(1)
	var once sync.Once
	return func(err error) {
		once.Do(func() {
			s.inflight.Add(-1)
			if err != nil {
				s.err.Add(1)
			} else {
				s.ok.Add(1)
			}
		})
	}
}

// Serve marks a request served by the server name until the returned
// function is called with its outcome. Call it where the request arrives,
// before it waits for a worker or a lock.
func Serve(ctx context.Context, name string) func(err error) {
	r := From(ctx)
	return track(r.get(r.serve, name))
}

// Operation marks a client's operation on service, all its attempts and
// the waits between them, until the returned function is called.
func Operation(ctx context.Context, service string) func(err error) {
	r := From(ctx)
	return track(r.get(r.ops, service))
}

// Call marks one attempt of an operation on service, until its response or
// timeout.
func Call(ctx context.Context, service string) func(err error) {
	r := From(ctx)
	return track(r.get(r.calls, service))
}

// Queue is u: the requests the server name is serving now.
func (r *Roles) Queue(name string) int { return int(r.get(r.serve, name).inflight.Load()) }

// Orbit is v: operations on service in progress with no attempt in flight.
func (r *Roles) Orbit(service string) int {
	n := r.get(r.ops, service).inflight.Load() - r.get(r.calls, service).inflight.Load()
	return int(max(n, 0))
}

// Served counts the server's completed requests by outcome.
func (r *Roles) Served(name string) (ok, failed int) {
	s := r.get(r.serve, name)
	return int(s.ok.Load()), int(s.err.Load())
}

// Attempts counts the attempts on service by outcome.
func (r *Roles) Attempts(service string) (ok, failed int) {
	s := r.get(r.calls, service)
	return int(s.ok.Load()), int(s.err.Load())
}

// Metric names, as OTel instruments (Prometheus: dots become underscores).
const (
	MetricServeInflight = "quint.serve.inflight" // u, by name
	MetricOrbit         = "quint.orbit"          // v, by service
	MetricServed        = "quint.serve.requests" // counter, by name and outcome
	MetricAttempts      = "quint.call.attempts"  // counter, by service and outcome
)

// Instrument registers the registry's gauges and counters with meter.
func (r *Roles) Instrument(meter metric.Meter) error {
	u, err := meter.Int64ObservableGauge(MetricServeInflight, metric.WithDescription("requests a server is serving (qmeta's u)"))
	if err != nil {
		return err
	}
	v, err := meter.Int64ObservableGauge(MetricOrbit, metric.WithDescription("operations waiting to retry (qmeta's v)"))
	if err != nil {
		return err
	}
	served, err := meter.Int64ObservableCounter(MetricServed, metric.WithDescription("requests served, by outcome"))
	if err != nil {
		return err
	}
	attempts, err := meter.Int64ObservableCounter(MetricAttempts, metric.WithDescription("attempts, by outcome"))
	if err != nil {
		return err
	}
	_, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		for _, name := range r.names(r.serve) {
			o.ObserveInt64(u, int64(r.Queue(name)), metric.WithAttributes(attribute.String("name", name)))
			ok, failed := r.Served(name)
			o.ObserveInt64(served, int64(ok), metric.WithAttributes(attribute.String("name", name), attribute.String("outcome", "ok")))
			o.ObserveInt64(served, int64(failed), metric.WithAttributes(attribute.String("name", name), attribute.String("outcome", "error")))
		}
		for _, svc := range r.names(r.ops) {
			o.ObserveInt64(v, int64(r.Orbit(svc)), metric.WithAttributes(attribute.String("service", svc)))
		}
		for _, svc := range r.names(r.calls) {
			ok, failed := r.Attempts(svc)
			o.ObserveInt64(attempts, int64(ok), metric.WithAttributes(attribute.String("service", svc), attribute.String("outcome", "ok")))
			o.ObserveInt64(attempts, int64(failed), metric.WithAttributes(attribute.String("service", svc), attribute.String("outcome", "error")))
		}
		return nil
	}, u, v, served, attempts)
	return err
}

func (r *Roles) names(m map[string]*stats) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(m))
	for n := range m {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
