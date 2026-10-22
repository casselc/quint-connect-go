// Package guarded uses sync in the ways Orchestrion's matching misses:
// a lock embedded in a struct (promoted calls), a method value returned as
// the unlock function, and a goroutine started with go.
package guarded

import (
	"context"
	"sync"
	"time"

	"github.com/casselc/quint-connect-go/qenv"
)

// Counter embeds its lock: c.Lock() is a promoted call.
type Counter struct {
	sync.Mutex
	N int
}

// Store hands out its read lock's unlock as a method value.
type Store struct {
	mu   sync.RWMutex
	Rows map[string]int
}

// RLock takes the read lock and returns the method value that releases it.
func (s *Store) RLock() func() {
	s.mu.RLock()
	return s.mu.RUnlock
}

// Put writes a row under the write lock.
func (s *Store) Put(ctx context.Context, k string, v int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	qenv.Sleep(ctx, time.Millisecond) // the simulation runs others meanwhile
	s.Rows[k] = v
}

// Inc increments under the embedded lock, slowly.
func (c *Counter) Inc(ctx context.Context) {
	c.Lock()
	n := c.N
	qenv.Sleep(ctx, time.Millisecond)
	c.N = n + 1
	c.Unlock()
}

// Spawn runs fn in a goroutine of its own.
func Spawn(wg *sync.WaitGroup, fn func()) {
	wg.Add(1)
	//qenv:go
	go func() {
		defer wg.Done()
		fn()
	}()
}
