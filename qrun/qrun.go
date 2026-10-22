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
	"context"
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

// Current is the calling goroutine's run context, if it belongs to a run.
func Current() (context.Context, bool) {
	if f := resolver.Load(); f != nil {
		return (*f)()
	}
	return nil, false
}

// Task identifies the simulation task a context belongs to (qenv sets it
// on every task's context): state kept per task, such as the code a task
// ran since its last step.
var Task = NewKey[any]("task")
