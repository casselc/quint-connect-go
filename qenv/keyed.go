package qenv

import (
	"context"
	"fmt"
)

// Locks and wait groups keyed by the real object they stand for: what code
// rewritten at build time (qenv/weave) calls in place of sync.Mutex,
// sync.RWMutex and sync.WaitGroup methods, so that blocking on them is
// waiting in the simulation, and taking them is a scheduling point Hegel or
// a seed chooses around. The real object is only a key; its own state is
// never touched while its users run in the simulation.
//
// The semantics are sync's: a waiting writer blocks new readers, an unlock
// of an unlocked mutex and a negative wait group counter panic.

type rwState struct {
	writer         bool
	readers        int
	writersWaiting int
}

type wgState struct{ n int }

func keyedOf[T any](ctx context.Context, key any, what string) (*T, *task) {
	t := taskOf(ctx)
	if t == nil {
		panic(fmt.Sprintf("qenv: %s outside a simulation task", what))
	}
	s := t.sim
	if s.keyed == nil {
		s.keyed = map[any]any{}
	}
	v, ok := s.keyed[key].(*T)
	if !ok {
		v = new(T)
		s.keyed[key] = v
	}
	return v, t
}

// LockKey takes the lock named by key exclusively (Mutex.Lock,
// RWMutex.Lock).
func LockKey(ctx context.Context, key any) {
	l, _ := keyedOf[rwState](ctx, key, "Lock")
	Yield(ctx)
	l.writersWaiting++
	Wait(ctx, func() bool { return !l.writer && l.readers == 0 })
	l.writersWaiting--
	l.writer = true
}

// TryLockKey takes the lock exclusively if it is free.
func TryLockKey(ctx context.Context, key any) bool {
	l, _ := keyedOf[rwState](ctx, key, "TryLock")
	Yield(ctx)
	if l.writer || l.readers > 0 {
		return false
	}
	l.writer = true
	return true
}

// UnlockKey releases an exclusive hold.
func UnlockKey(ctx context.Context, key any) {
	l, _ := keyedOf[rwState](ctx, key, "Unlock")
	if !l.writer {
		panic("qenv: unlock of unlocked mutex")
	}
	l.writer = false
}

// RLockKey takes the lock shared (RWMutex.RLock): it waits while a writer
// holds it or waits for it.
func RLockKey(ctx context.Context, key any) {
	l, _ := keyedOf[rwState](ctx, key, "RLock")
	Yield(ctx)
	Wait(ctx, func() bool { return !l.writer && l.writersWaiting == 0 })
	l.readers++
}

// TryRLockKey takes the lock shared if no writer holds or waits for it.
func TryRLockKey(ctx context.Context, key any) bool {
	l, _ := keyedOf[rwState](ctx, key, "TryRLock")
	Yield(ctx)
	if l.writer || l.writersWaiting > 0 {
		return false
	}
	l.readers++
	return true
}

// RUnlockKey releases a shared hold.
func RUnlockKey(ctx context.Context, key any) {
	l, _ := keyedOf[rwState](ctx, key, "RUnlock")
	if l.readers == 0 {
		panic("qenv: RUnlock of unlocked RWMutex")
	}
	l.readers--
}

// AddKey adds n to the wait group named by key (WaitGroup.Add, Done).
func AddKey(ctx context.Context, key any, n int) {
	g, _ := keyedOf[wgState](ctx, key, "WaitGroup.Add")
	g.n += n
	if g.n < 0 {
		panic("qenv: negative WaitGroup counter")
	}
}

// WaitKey waits until the wait group's counter is zero (WaitGroup.Wait).
func WaitKey(ctx context.Context, key any) {
	g, _ := keyedOf[wgState](ctx, key, "WaitGroup.Wait")
	Yield(ctx)
	Wait(ctx, func() bool { return g.n == 0 })
}
