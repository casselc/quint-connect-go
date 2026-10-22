package qenv_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/casselc/quint-connect-go/qenv"
)

// A task waiting on a Signal nobody notifies once ended the run as if it
// had quiesced: Run returned nil and the test checked a run that stopped
// half way. Now Run reports it, and names the task.
func TestRunReportsStuckTasks(t *testing.T) {
	s := qenv.New(qenv.Nominal{})
	defer s.Close()
	var never qenv.Signal
	s.Spawn("waiter", func(ctx context.Context) { never.Wait(ctx, time.Time{}) })
	err := s.Run(time.Second)
	if !errors.Is(err, qenv.ErrStuck) || !strings.Contains(err.Error(), "waiter") {
		t.Fatalf("got %v, want ErrStuck naming waiter", err)
	}
}

// A daemon's wait, a queue's receive and a wait with a deadline may last:
// none of them is stuck.
func TestDaemonsAndReceivesAreNotStuck(t *testing.T) {
	s := qenv.New(qenv.Nominal{})
	defer s.Close()
	var never qenv.Signal
	q := qenv.NewQueue(s, "q")
	s.Spawn("server", func(ctx context.Context) {
		qenv.Daemon(ctx)
		never.Wait(ctx, time.Time{})
	})
	s.Spawn("consumer", func(ctx context.Context) { q.Receive(ctx) })
	if err := s.Run(time.Second); err != nil {
		t.Fatal(err)
	}
}

// Outside a simulation Wait and Signal.Wait are real waits: a seam's code
// runs unchanged with a real client, whose goroutines notify.
func TestWaitOutsideASimulation(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	done := false
	var sig qenv.Signal
	go func() { //qenv:ok a real goroutine on purpose: the wait is outside any simulation
		time.Sleep(5 * time.Millisecond) //qenv:ok real time on purpose
		mu.Lock()
		done = true
		mu.Unlock()
		sig.Notify()
	}()
	if !sig.Wait(ctx, time.Now().Add(5*time.Second)) { //qenv:ok a real deadline
		t.Fatal("Signal.Wait: not notified")
	}
	qenv.Wait(ctx, func() bool { mu.Lock(); defer mu.Unlock(); return done })
	if qenv.WaitUntil(ctx, func() bool { return false }, time.Now().Add(10*time.Millisecond)) { //qenv:ok a real deadline
		t.Error("WaitUntil past its deadline reported ready")
	}
}
