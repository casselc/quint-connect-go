// Package qrun finds the state of the run code belongs to: a recorder, a
// role registry, coverage counts, anything a run carries for its code. A
// value is looked up in the context first and, when the context does not
// carry it (context.Background() in a library, context.TODO() in woven
// code), in the context of the run the calling goroutine belongs to, which
// the runtime (qenv) resolves from the goroutine. So runs in parallel keep
// their state apart wherever their code gets its context from.
//
// One mechanism for every package that needs it (qobs, qrole, qcover)
// replaced one per package, each with its own fallback and one of them
// without any (qrole counted woven calls into a shared default).
package qrun

import (
	"bytes"
	"context"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
)

// A Key names a value of type T a run carries. Keys compare by identity:
// make each once, with NewKey.
type Key[T any] struct{ name string }

// NewKey returns a new key; name is for diagnostics.
func NewKey[T any](name string) *Key[T] { return &Key[T]{name} }

func (k *Key[T]) String() string { return "qrun." + k.name }

// With returns ctx carrying v for k.
func With[T any](ctx context.Context, k *Key[T], v T) context.Context {
	return context.WithValue(ctx, k, v)
}

// In returns ctx's value for k, without the goroutine's run.
func In[T any](ctx context.Context, k *Key[T]) (T, bool) {
	if ctx != nil {
		if v, ok := ctx.Value(k).(T); ok {
			return v, true
		}
	}
	var zero T
	return zero, false
}

// Get returns the value for k of ctx or, if ctx (nil allowed) carries
// none, of the calling goroutine's run.
func Get[T any](ctx context.Context, k *Key[T]) (T, bool) {
	if v, ok := In(ctx, k); ok {
		return v, true
	}
	if rc, ok := Current(); ok {
		return In(rc, k)
	}
	var zero T
	return zero, false
}

var resolver atomic.Pointer[func() (context.Context, bool)]

// SetResolver installs how the calling goroutine's run context is found:
// qenv returns the context of the goroutine's simulation task, or of the
// simulation it created.
func SetResolver(f func() (context.Context, bool)) { resolver.Store(&f) }

// Current is the calling goroutine's run context, if it belongs to a run:
// a simulation's (the resolver), else one Start began on it.
func Current() (context.Context, bool) {
	if f := resolver.Load(); f != nil {
		if ctx, ok := (*f)(); ok {
			return ctx, true
		}
	}
	if started.n.Load() == 0 {
		return nil, false
	}
	started.Lock()
	defer started.Unlock()
	if st := started.m[GoroutineID()]; len(st) > 0 {
		return st[len(st)-1], true
	}
	return nil, false
}

// started holds the runs Start began, a stack per goroutine.
var started struct {
	sync.Mutex
	m map[int64][]context.Context
	n atomic.Int64 // with none, Current costs no goroutine lookup
}

// Start makes ctx the calling goroutine's run until end is called: code on
// this goroutine that finds its state through Get (qobs.Record with
// context.Background(), woven //quint:action) finds ctx's, and a simulation
// created here (qenv.New) inherits it. A run without a simulation (a test
// recording a library's steps) needs it to stay apart from runs in
// parallel. Goroutines the run starts are not in it: they have only the
// contexts they are given.
func Start(ctx context.Context) (end func()) {
	id := GoroutineID()
	started.Lock()
	if started.m == nil {
		started.m = map[int64][]context.Context{}
	}
	started.m[id] = append(started.m[id], ctx)
	started.n.Add(1)
	started.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			started.Lock()
			defer started.Unlock()
			st := started.m[id]
			for i := len(st) - 1; i >= 0; i-- {
				if st[i] == ctx {
					st = append(st[:i], st[i+1:]...)
					break
				}
			}
			if len(st) == 0 {
				delete(started.m, id)
			} else {
				started.m[id] = st
			}
			started.n.Add(-1)
		})
	}
}

// GoroutineID is the calling goroutine's id, from the runtime's stack
// header ("goroutine 123 [running]:"): Go has no goroutine-local storage.
func GoroutineID() int64 {
	var buf [32]byte
	b := buf[:runtime.Stack(buf[:], false)]
	b = bytes.TrimPrefix(b, []byte("goroutine "))
	if i := bytes.IndexByte(b, ' '); i > 0 {
		b = b[:i]
	}
	id, _ := strconv.ParseInt(string(b), 10, 64)
	return id
}

// Task identifies the simulation task a context belongs to (qenv sets it
// on every task's context): state kept per task, such as the code a task
// ran since its last step.
var Task = NewKey[any]("task")
