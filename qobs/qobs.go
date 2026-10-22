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
	"github.com/casselc/quint-connect-go/qrun"
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
	added   atomic.Uint64 // steps written by Add
	sink    Sink
	actor   string
	process string

	mu          sync.Mutex
	interrupted []qtrace.Step    // SetDiscard
	threads     map[string]int64 // a Thread's goroutine
	issues      []string
}

// Issues are what the recorder found wrong with how it was used, as it
// recorded: a Thread used from two goroutines (a thread asserts program
// order, which two goroutines do not have). An exploration fails a run
// with issues.
func (r *Recorder) Issues() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.issues...)
}

// checkThread notes the goroutine of a Thread's first step, and an issue
// when a later step comes from another.
func (r *Recorder) checkThread(thread, action string) {
	g := qrun.GoroutineID()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.threads == nil {
		r.threads = map[string]int64{}
	}
	first, ok := r.threads[thread]
	switch {
	case !ok:
		r.threads[thread] = g
	case first != g && len(r.issues) < 10:
		r.issues = append(r.issues, fmt.Sprintf("thread %s (qobs.Thread) recorded %q from goroutine %d after goroutine %d: "+
			"a thread asserts program order, which two goroutines do not have; give each its own (qobs.Thread in the goroutine)", thread, action, g, first))
		r.threads[thread] = g // one issue per change of goroutine
	}
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

// Count is how many steps the recorder has recorded (Add's included): a
// run's progress, which its choices are aligned to (qenv.Choice.Step).
func (r *Recorder) Count() int { return int(r.seq.Load() + r.added.Load()) }

// ID is the recorder id (the scope of quint.seq).
func (r *Recorder) ID() string { return r.id }

// Add writes steps recorded elsewhere to the recorder's sink as they are,
// with their own recorder ids and sequence numbers: steps a simulation in
// another process sent back (gosim's, see examples/gosim).
func (r *Recorder) Add(steps ...qtrace.Step) {
	for i := range steps {
		r.sink.Record(context.Background(), &steps[i])
		r.added.Add(1)
	}
}

var global atomic.Pointer[Recorder]

// SetGlobal installs the recorder used by the package-level Record (and so by
// woven //quint:action advice). nil turns recording off.
func SetGlobal(r *Recorder) { global.Store(r) }

// Global returns the installed recorder, or nil.
func Global() *Recorder { return global.Load() }

// installed is the recorder InstallGlobal put in place, while it is.
var installed atomic.Pointer[Recorder]

// InstallGlobal installs r as the global recorder for a test's run
// (qtest.Global, quinthegel's Config.Global): the steps of goroutines no
// run can follow (a server's, a cluster's) go to it. A second install
// while one is in place fails: the two runs would take each other's
// steps, so tests that install it must not run in parallel. restore puts
// back the recorder that was there before, unless something else has
// replaced r since (it is not undone by identity, then: a test's
// restore cannot remove another's recorder).
func InstallGlobal(r *Recorder) (restore func(), err error) {
	if r == nil {
		return nil, errors.New("qobs: InstallGlobal(nil)")
	}
	if !installed.CompareAndSwap(nil, r) {
		return nil, errors.New("qobs: another run's global recorder is installed: runs that install one (qtest.Global, " +
			"quinthegel's Config.Global) must not run in parallel; drop t.Parallel, or record through the run's context")
	}
	prev := global.Swap(r)
	var once sync.Once
	return func() {
		once.Do(func() {
			global.CompareAndSwap(r, prev)
			installed.CompareAndSwap(r, nil)
		})
	}, nil
}

var recorderKey = qrun.NewKey[*Recorder]("recorder")

// WithRecorder returns a context whose steps Record sends to r rather than
// to the global recorder. Runs in parallel (seeds of an exploration,
// simulations of a trigger search) each record into their own: a qenv
// simulation passes its context to every task (qenv.WithContext).
func WithRecorder(ctx context.Context, r *Recorder) context.Context {
	return qrun.With(ctx, recorderKey, r)
}

var (
	discard atomic.Pointer[func() bool]
	strict  atomic.Int64
)

// SetDiscard installs a check, made for every step, of whether the calling
// goroutine is unwinding work that never completed. A qenv task killed by
// a crash stays frozen; when the simulation is closed it unwinds, and runs
// its deferred functions. A step recorded in one (a //quint:action
// function's, which records on return) would claim a completion that did
// not happen: it goes to the recorder's Interrupted list, not to its sink.
// qenv installs the check.
func SetDiscard(f func() bool) { discard.Store(&f) }

// Strict turns on, until release is called, the rule that a step must have
// somewhere to go: a step with no recorder in its context, no run for its
// goroutine and no global recorder is logged as escaped (Escaped) instead
// of being dropped silently. Simulated runs (qtest.Explore,
// qload.Experiment) are strict, and fail when a step escaped: a dropped
// step would make a correct run fail the model, or hide a bug. It is a
// log, not a panic: the step comes from a goroutine no run owns, whose
// panic nothing could recover, and it would kill the test binary.
func Strict() (release func()) {
	strict.Add(1)
	var once sync.Once
	return func() { once.Do(func() { strict.Add(-1) }) }
}

// From returns the recorder Record uses for ctx: the one WithRecorder set;
// else that of the run the goroutine belongs to (qrun.Get: a step recorded
// with context.Background() inside a simulation still lands in its own
// run, even with runs in parallel); else the global one (nil if none).
func From(ctx context.Context) *Recorder {
	if r, ok := qrun.Get(ctx, recorderKey); ok && r != nil {
		return r
	}
	return global.Load()
}

// Record records one step through the context's recorder (WithRecorder),
// its goroutine's run's (qrun) or the global one; a no-op when
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
		escaped.Lock()
		escaped.n++
		if len(escaped.msgs) < maxEscaped {
			escaped.msgs = append(escaped.msgs, fmt.Sprintf("qobs: step %q has no recorder: its context carries none, its goroutine belongs to no "+
				"simulated run, and no global recorder is installed. A goroutine started with `go` is outside the "+
				"simulation: start it with qenv.Go, or record through a context from the run (qenv.WithContext).", action))
		}
		escaped.Unlock()
	}
}

// escaped logs the steps strict runs could not place, process-wide: their
// goroutine names no run, so no run can be told.
var escaped struct {
	sync.Mutex
	n    int
	msgs []string // the first maxEscaped
}

const maxEscaped = 1000

// Escaped returns the number of steps that escaped strict runs so far,
// and messages of those after mark (an earlier count): a strict run takes
// the count when it starts and reports what escaped while it ran. Compare
// the counts: past the first 1000 escapes the messages are not kept, and
// since then holds one that says so.
func Escaped(mark int) (n int, since []string) {
	escaped.Lock()
	defer escaped.Unlock()
	if mark < len(escaped.msgs) {
		since = append(since, escaped.msgs[mark:]...)
	}
	if escaped.n > mark && len(since) == 0 {
		since = []string{fmt.Sprintf("qobs: %d steps had no recorder (their messages are past the first %d kept): "+
			"a goroutine started with `go` is outside the simulation; start it with qenv.Go", escaped.n-mark, maxEscaped)}
	}
	return escaped.n, since
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
		r.checkThread(t, action)
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
