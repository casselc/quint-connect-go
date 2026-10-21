// Package qobs is the runtime half of quintgo: it records model steps from a
// running Go program. It is what the //quint:action Orchestrion aspect calls,
// and it can be called directly.
//
// Recording is off until SetGlobal installs a Recorder, a context carries
// one (WithRecorder), or a Recorder is used directly, so linking qobs into a
// program changes nothing by itself.
package qobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/casselc/quint-connect-go/qtrace"
	"go.opentelemetry.io/otel/trace"
)

// Sink receives recorded steps. Record may be called concurrently.
type Sink interface {
	Record(ctx context.Context, s *qtrace.Step)
}

// Outcomer lets an error classify itself for the model, e.g. an S3 timeout
// that is known to have been applied reports "ambiguous". Without it a
// non-nil error is recorded as "error".
type Outcomer interface{ QuintOutcome() string }

// Recorder assigns each step a dense sequence number and hands it to a sink.
type Recorder struct {
	id      string
	seq     atomic.Uint64
	sink    Sink
	actor   string
	process string

	mu          sync.Mutex
	interrupted []qtrace.Step // SetDiscard
}

// Interrupted returns the steps this recorder was asked to record by work
// that never completed (a qenv task unwinding after a crash or at the end
// of a run: SetDiscard), in the order asked. They are not in the run and
// not checked against the model: they say what a crash or the end of the
// run cut off, for reports.
func (r *Recorder) Interrupted() []qtrace.Step {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]qtrace.Step(nil), r.interrupted...)
}

// Option configures a Recorder.
type Option func(*Recorder)

// WithActor sets the default quint.actor of every step.
func WithActor(a string) Option { return func(r *Recorder) { r.actor = a } }

// WithProcess sets the default quint.process (incarnation) of every step.
// It defaults to the recorder id, which is fresh per Recorder.
func WithProcess(p string) Option { return func(r *Recorder) { r.process = p } }

// WithID sets the recorder id (default: random).
func WithID(id string) Option { return func(r *Recorder) { r.id = id } }

// NewRecorder returns a Recorder writing to sink.
func NewRecorder(sink Sink, opts ...Option) *Recorder {
	var b [6]byte
	_, _ = rand.Read(b[:])
	r := &Recorder{id: hex.EncodeToString(b[:]), sink: sink}
	for _, o := range opts {
		o(r)
	}
	if r.process == "" {
		r.process = r.id
	}
	if r.actor == "" {
		r.actor = "default" // as qtrace.FromAttributes reads a step without one
	}
	return r
}

// ID is the recorder id (the scope of quint.seq).
func (r *Recorder) ID() string { return r.id }

var global atomic.Pointer[Recorder]

// SetGlobal installs the recorder used by the package-level Record (and so by
// woven //quint:action advice). nil turns recording off.
func SetGlobal(r *Recorder) { global.Store(r) }

// Global returns the installed recorder, or nil.
func Global() *Recorder { return global.Load() }

type recorderKey struct{}

// WithRecorder returns a context whose steps Record sends to r rather than
// to the global recorder. Runs in parallel (seeds of an exploration,
// simulations of a trigger search) each record into their own: a qenv
// simulation passes its context to every task (qenv.WithContext).
func WithRecorder(ctx context.Context, r *Recorder) context.Context {
	return context.WithValue(ctx, recorderKey{}, r)
}

// InContext returns the recorder WithRecorder put in ctx, or nil.
func InContext(ctx context.Context) *Recorder {
	if ctx != nil {
		if r, ok := ctx.Value(recorderKey{}).(*Recorder); ok {
			return r
		}
	}
	return nil
}

var (
	fallback atomic.Pointer[func() (*Recorder, bool)]
	discard  atomic.Pointer[func() bool]
	strict   atomic.Int64
)

// SetDiscard installs a check, made for every step, of whether the calling
// goroutine is unwinding work that never completed. A qenv task killed by
// a crash stays frozen; when the simulation is closed it unwinds, and runs
// its deferred functions. A step recorded in one (a //quint:action
// function's, which records on return) would claim a completion that did
// not happen: it goes to the recorder's Interrupted list, not to its sink.
// qenv installs the check.
func SetDiscard(f func() bool) { discard.Store(&f) }

// SetFallback installs where a step whose context carries no recorder goes
// before the global recorder: f returns the recorder of the run the calling
// goroutine belongs to, and whether it belongs to one. qenv installs it, so
// that a step recorded with context.Background() inside a simulation still
// lands in its own run, even with runs in parallel.
func SetFallback(f func() (*Recorder, bool)) { fallback.Store(&f) }

// Strict turns on, until release is called, the rule that a step must have
// somewhere to go: a step with no recorder in its context, no run for its
// goroutine and no global recorder panics instead of being dropped.
// Simulated runs (qtest.Explore, qload.Experiment) are strict: a dropped
// step would make a correct run fail the model, or hide a bug.
func Strict() (release func()) {
	strict.Add(1)
	var once sync.Once
	return func() { once.Do(func() { strict.Add(-1) }) }
}

// From returns the recorder Record uses for ctx: the one WithRecorder set;
// else that of the run the goroutine belongs to (SetFallback); else the
// global one (nil if none).
func From(ctx context.Context) *Recorder {
	if r := InContext(ctx); r != nil {
		return r
	}
	if f := fallback.Load(); f != nil {
		if r, ok := (*f)(); ok {
			return r
		}
	}
	return global.Load()
}

// Record records one step through the context's recorder (WithRecorder),
// its goroutine's run's (SetFallback) or the global one; a no-op when
// there is none, unless a run is Strict. This is the function
// //quint:action advice calls.
func Record(ctx context.Context, action string, err error, kv ...any) {
	if r := From(ctx); r != nil {
		r.Record(ctx, action, err, kv...)
		return
	}
	if f := discard.Load(); f != nil && (*f)() {
		return // interrupted work with nowhere to report it
	}
	if strict.Load() > 0 {
		panic(fmt.Sprintf("qobs: step %q has no recorder: its context carries none, its goroutine belongs to no "+
			"simulated run, and no global recorder is installed. A goroutine started with `go` is outside the "+
			"simulation: start it with qenv.Go, or record through a context from the run (qenv.WithContext).", action))
	}
}

type threadKey struct{}

var threadSeq atomic.Uint64

// Thread returns a context whose steps are in program order with each other
// (quint.thread). Use it where one goroutine performs a sequence of steps and
// no OTel span marks that sequence. Without it, the span in ctx is the thread.
func Thread(ctx context.Context) context.Context {
	return context.WithValue(ctx, threadKey{}, fmt.Sprintf("t%d", threadSeq.Add(1)))
}

// Record records one step. kv alternates keys and values:
//
//	"actor", "process", "thread", "outcome"  override the step's identity
//	"when"        "ok" | "error": record only if err is nil / non-nil
//	"obs.<name>"  an observed projection of the state after the step
//	"order.<d>"   a domain logical clock (see qtrace.Reconstruct)
//	anything else an argument of the action (quint.arg.<key>)
//
// Values are normalised to string, int64, bool or float64.
func (r *Recorder) Record(ctx context.Context, action string, err error, kv ...any) {
	if r == nil {
		return
	}
	s, ok := r.step(ctx, action, err, kv)
	if !ok {
		return
	}
	if f := discard.Load(); f != nil && (*f)() {
		s.Time = time.Now()
		r.mu.Lock()
		r.interrupted = append(r.interrupted, *s)
		r.mu.Unlock()
		return
	}
	// seq is taken here, after the effect the step describes has happened.
	// Two concurrent steps may be numbered in either order; the
	// reconstructor only relies on seq within a thread (see qtrace).
	s.Seq = r.seq.Add(1)
	s.Time = time.Now()
	r.sink.Record(ctx, s)
}

// step builds a step from Record's arguments; false if "when" excludes it.
func (r *Recorder) step(ctx context.Context, action string, err error, kv []any) (*qtrace.Step, bool) {
	s := qtrace.Step{Action: action, Recorder: r.id, Actor: r.actor, Process: r.process, Outcome: outcome(err)}
	if err != nil {
		s.Error = err.Error()
	}
	if t, ok := ctx.Value(threadKey{}).(string); ok {
		s.Thread = t
	} else if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		s.Thread = sc.SpanID().String()
	}
	for i := 0; i+1 < len(kv); i += 2 {
		k, _ := kv[i].(string)
		v := Normalize(kv[i+1])
		switch {
		case k == "when":
			if (v == "ok" && err != nil) || (v == "error" && err == nil) {
				return nil, false
			}
		case k == "actor":
			s.Actor = fmt.Sprint(v)
		case k == "process":
			s.Process = fmt.Sprint(v)
		case k == "thread":
			s.Thread = fmt.Sprint(v)
		case k == "outcome":
			s.Outcome = fmt.Sprint(v)
		case len(k) > 4 && k[:4] == "obs.":
			if s.Obs == nil {
				s.Obs = map[string]any{}
			}
			s.Obs[k[4:]] = v
		case len(k) > 6 && k[:6] == "order.":
			if n, ok := v.(int64); ok {
				if s.Order == nil {
					s.Order = map[string]int64{}
				}
				s.Order[k[6:]] = n
			}
		default:
			if s.Args == nil {
				s.Args = map[string]any{}
			}
			s.Args[k] = v
		}
	}
	return &s, true
}

func outcome(err error) string {
	if err == nil {
		return "ok"
	}
	var o Outcomer
	if errors.As(err, &o) {
		return o.QuintOutcome()
	}
	return "error"
}

// Normalize maps a Go value to an attribute-friendly one.
func Normalize(v any) any {
	switch x := v.(type) {
	case nil:
		return ""
	case string, bool, int64, float64:
		return x
	case int:
		return int64(x)
	case int8:
		return int64(x)
	case int16:
		return int64(x)
	case int32:
		return int64(x)
	case uint:
		return int64(x)
	case uint8:
		return int64(x)
	case uint16:
		return int64(x)
	case uint32:
		return int64(x)
	case uint64:
		return int64(x)
	case float32:
		return float64(x)
	case fmt.Stringer:
		return x.String()
	case error:
		return x.Error()
	}
	// Collections (slices, arrays, maps) travel as JSON text: an attribute
	// or step-log field holds a scalar. A binding's toSet / toList turn it
	// back into a Quint value.
	switch reflect.ValueOf(v).Kind() {
	case reflect.Slice, reflect.Array, reflect.Map:
		if b, err := json.Marshal(v); err == nil {
			return string(b)
		}
	}
	return fmt.Sprint(v)
}

// MemorySink keeps steps in memory: for tests, and for validating a run
// in-process without any OTel pipeline.
type MemorySink struct {
	mu    sync.Mutex
	steps []qtrace.Step
}

func (m *MemorySink) Record(_ context.Context, s *qtrace.Step) {
	m.mu.Lock()
	m.steps = append(m.steps, *s)
	m.mu.Unlock()
}

// Steps returns a copy of what was recorded.
func (m *MemorySink) Steps() []qtrace.Step {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]qtrace.Step(nil), m.steps...)
}

// Reset forgets everything recorded.
func (m *MemorySink) Reset() { m.mu.Lock(); m.steps = nil; m.mu.Unlock() }

// Tee records to several sinks.
type Tee []Sink

func (t Tee) Record(ctx context.Context, s *qtrace.Step) {
	for _, x := range t {
		x.Record(ctx, s)
	}
}
