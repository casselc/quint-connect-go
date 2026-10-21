// Package weave brings code that uses plain Go concurrency (sync.Mutex,
// sync.RWMutex, sync.WaitGroup and the go statement) into a qenv
// simulation without changing its source. Built with Orchestrion, with
// this package imported in the project's orchestrion.tool.go, the method
// calls of the root module's packages become calls of the functions here,
// and a go statement marked //qenv:go starts a simulation task:
//
//	//qenv:go
//	go func() { ... }()
//
// Each function checks whether the calling goroutine is a task of a
// running simulation (qenv.Current). If it is, the lock or wait group is
// the simulation's, keyed by the real object (qenv.LockKey and friends):
// taking it is a scheduling point and blocking on it is a simulated wait.
// If not, the real method runs. A woven program therefore behaves as
// before outside a simulation, at the cost of a goroutine lookup per call:
// weave test builds, not production ones.
//
// Inside a simulation, every user of a woven object must be one of its
// tasks: a lock taken in a task and released by the goroutine that created
// the simulation would be released on the real mutex, which was never
// taken.
package weave

import (
	"context"
	"sync"

	"github.com/casselc/quint-connect-go/qenv"
)

// Go is a go statement marked //qenv:go: in a simulation, fn runs as a new
// task and the go statement starts a goroutine that does nothing;
// outside, fn runs on that goroutine as written. It is evaluated by the
// goroutine executing the go statement, before the new one starts, which
// is what lets it find the simulation.
func Go(fn func()) func() {
	if ctx, ok := qenv.Current(); ok {
		qenv.Go(ctx, "go", func(context.Context) { fn() })
		return func() {}
	}
	return fn
}

// MutexLock is (*sync.Mutex).Lock.
func MutexLock(m *sync.Mutex) {
	if ctx, ok := qenv.Current(); ok {
		qenv.LockKey(ctx, m)
		return
	}
	m.Lock()
}

// MutexUnlock is (*sync.Mutex).Unlock.
func MutexUnlock(m *sync.Mutex) {
	if ctx, ok := qenv.Current(); ok {
		qenv.UnlockKey(ctx, m)
		return
	}
	m.Unlock()
}

// MutexTryLock is (*sync.Mutex).TryLock.
func MutexTryLock(m *sync.Mutex) bool {
	if ctx, ok := qenv.Current(); ok {
		return qenv.TryLockKey(ctx, m)
	}
	return m.TryLock()
}

// RWLock is (*sync.RWMutex).Lock.
func RWLock(m *sync.RWMutex) {
	if ctx, ok := qenv.Current(); ok {
		qenv.LockKey(ctx, m)
		return
	}
	m.Lock()
}

// RWUnlock is (*sync.RWMutex).Unlock.
func RWUnlock(m *sync.RWMutex) {
	if ctx, ok := qenv.Current(); ok {
		qenv.UnlockKey(ctx, m)
		return
	}
	m.Unlock()
}

// RWTryLock is (*sync.RWMutex).TryLock.
func RWTryLock(m *sync.RWMutex) bool {
	if ctx, ok := qenv.Current(); ok {
		return qenv.TryLockKey(ctx, m)
	}
	return m.TryLock()
}

// RLock is (*sync.RWMutex).RLock.
func RLock(m *sync.RWMutex) {
	if ctx, ok := qenv.Current(); ok {
		qenv.RLockKey(ctx, m)
		return
	}
	m.RLock()
}

// RUnlock is (*sync.RWMutex).RUnlock.
func RUnlock(m *sync.RWMutex) {
	if ctx, ok := qenv.Current(); ok {
		qenv.RUnlockKey(ctx, m)
		return
	}
	m.RUnlock()
}

// TryRLock is (*sync.RWMutex).TryRLock.
func TryRLock(m *sync.RWMutex) bool {
	if ctx, ok := qenv.Current(); ok {
		return qenv.TryRLockKey(ctx, m)
	}
	return m.TryRLock()
}

// Add is (*sync.WaitGroup).Add.
func Add(wg *sync.WaitGroup, n int) {
	if ctx, ok := qenv.Current(); ok {
		qenv.AddKey(ctx, wg, n)
		return
	}
	wg.Add(n)
}

// Done is (*sync.WaitGroup).Done.
func Done(wg *sync.WaitGroup) { Add(wg, -1) }

// Wait is (*sync.WaitGroup).Wait.
func Wait(wg *sync.WaitGroup) {
	if ctx, ok := qenv.Current(); ok {
		qenv.WaitKey(ctx, wg)
		return
	}
	wg.Wait()
}

// GroupGo is (*sync.WaitGroup).Go: f runs in a new task (or goroutine),
// counted by wg.
func GroupGo(wg *sync.WaitGroup, f func()) {
	Add(wg, 1)
	run := Go(func() {
		defer Done(wg)
		f()
	})
	go run() //qenv:ok an empty goroutine in a simulation (Go started the task); the real one outside
}
