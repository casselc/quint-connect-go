package weave_test

import (
	"context"
	"github.com/casselc/quint-connect-go/quinthegel"
	"sync"
	"testing"
	"time"

	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qenv/weave"
	"hegel.dev/go/hegel"
)

// What woven code calls, called directly: inside a simulation a real
// sync.RWMutex excludes as sync's does, under any schedule Hegel picks, with
// a waiting writer blocking new readers; a WaitGroup waits for tasks started
// by Go; outside a simulation the real methods run.
func TestWovenSyncInSimulation(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		s := qenv.New(quinthegel.Choices(ht))
		s.Check = true
		defer s.Close()
		var mu sync.RWMutex
		var wg sync.WaitGroup
		readers, writers, done := 0, 0, 0
		for i := range 4 {
			s.Spawn("task", func(ctx context.Context) {
				for range 3 {
					if i%2 == 0 {
						weave.RWLock(&mu)
						writers++
						if writers != 1 || readers != 0 {
							ht.Fatalf("writer with %d writers, %d readers", writers, readers)
						}
						qenv.Sleep(ctx, time.Millisecond)
						writers--
						weave.RWUnlock(&mu)
					} else {
						weave.RLock(&mu)
						readers++
						if writers != 0 {
							ht.Fatalf("reader with a writer")
						}
						qenv.Sleep(ctx, time.Millisecond)
						readers--
						weave.RUnlock(&mu)
					}
				}
				weave.Add(&wg, 1)
				go weave.Go(func() { defer weave.Done(&wg); done++ })() //qenv:ok what weaving makes of a //qenv:go statement
			})
		}
		s.Spawn("waiter", func(ctx context.Context) {
			qenv.Sleep(ctx, time.Second)
			weave.Wait(&wg)
			if done != 4 {
				ht.Fatalf("Wait returned with %d of 4 done", done)
			}
		})
		if err := s.Run(time.Minute); err != nil {
			ht.Fatal(err)
		}
	}, hegel.WithTestCases(100))

	// outside: the real mutex
	var mu sync.Mutex
	weave.MutexLock(&mu)
	if mu.TryLock() {
		t.Fatal("outside a simulation, MutexLock did not take the real mutex")
	}
	weave.MutexUnlock(&mu)
}

// A waiting writer blocks new readers, as with sync.RWMutex: a reader that
// arrives while a writer waits gets the lock after the writer.
func TestWriterPreference(t *testing.T) {
	s := qenv.New(qenv.Nominal{})
	defer s.Close()
	var mu sync.RWMutex
	var order []string
	s.Spawn("reader1", func(ctx context.Context) {
		weave.RLock(&mu)
		qenv.Sleep(ctx, 10*time.Millisecond)
		weave.RUnlock(&mu)
	})
	s.Spawn("writer", func(ctx context.Context) {
		qenv.Sleep(ctx, time.Millisecond)
		weave.RWLock(&mu)
		order = append(order, "writer")
		weave.RWUnlock(&mu)
	})
	s.Spawn("reader2", func(ctx context.Context) {
		qenv.Sleep(ctx, 2*time.Millisecond)
		weave.RLock(&mu)
		order = append(order, "reader2")
		weave.RUnlock(&mu)
	})
	if err := s.Run(time.Second); err != nil {
		t.Fatal(err)
	}
	if len(order) != 2 || order[0] != "writer" {
		t.Fatalf("order %v, want the writer before the reader that came while it waited", order)
	}
}
