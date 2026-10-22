package qenv_test

import (
	"context"
	"errors"
	"strings"
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
