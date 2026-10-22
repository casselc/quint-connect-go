package qenv_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/casselc/quint-connect-go/qenv"
)

// The native runtime on the wall clock: the same doubles and processes as
// on qenv's scheduler, on real goroutines and real (short) time. In gosim
// the same code runs on simulated time (examples/gosim).

// Read-modify-write increments with FOR UPDATE never lose an update, on
// real goroutines too: tasks still run one at a time between seams.
func TestNativeIncrements(t *testing.T) {
	for i := 0; i < 20; i++ {
		s := qenv.NewNative(qenv.NewSeeded(uint64(i)), nil)
		st := qenv.NewStore(s, "db", qenv.ReadCommitted)
		increments(s, st, true)
		if err := s.Run(50 * time.Millisecond); err != nil {
			t.Fatal(err)
		}
		v, _ := st.Read("n")
		s.Close()
		if v != 2 {
			t.Fatalf("run %d: n=%v, want 2", i, v)
		}
	}
}

// A crashed worker's transaction is released and its delivery redelivered
// to the restarted process; the crashing task stops at once.
func TestNativeCrashReleasesAndRedelivers(t *testing.T) {
	s := qenv.NewNative(qenv.Nominal{}, nil)
	st := qenv.NewStore(s, "db", qenv.ReadCommitted)
	q := qenv.NewQueue(s, "jobs")
	crashNext := true
	s.OnCrash(func(p *qenv.Process) { p.Restart() })
	var p *qenv.Process
	ranOn := false
	p = s.Process("worker", false, func(ctx context.Context) {
		for {
			d := q.Receive(ctx)
			tx := st.Begin(ctx)
			if _, _, err := tx.GetForUpdate("job"); err != nil {
				t.Error(err)
			}
			tx.Put("job", d.Body)
			if crashNext {
				crashNext = false
				p.Crash()
				ranOn = true
			}
			if err := tx.Commit(); err != nil {
				t.Error(err)
			}
			d.Ack(ctx)
		}
	})
	s.Spawn("client", func(ctx context.Context) { q.Publish(ctx, "j1") })
	if err := s.Run(200 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	v, _ := st.Read("job")
	redelivered, inc := q.Redelivered, p.Incarnation
	s.Close()
	if ranOn || v != "j1" || redelivered != 1 || inc != 2 {
		t.Fatalf("ran on=%v job=%v redelivered=%d incarnation=%d", ranOn, v, redelivered, inc)
	}
}

// A lost response is a timeout, after the request took effect, and the
// caller waited the timeout out.
func TestNativeLostResponse(t *testing.T) {
	applied := 0
	var got error
	var waited time.Duration
	s := qenv.NewNative(&forced{faults: map[string]int{"fault.rpc.pay.response": 1}}, nil)
	svc := qenv.NewService(s, "pay", nil, func(ctx context.Context, n int) (int, error) { applied += n; return applied, nil })
	s.Spawn("client", func(ctx context.Context) {
		start := qenv.Now(ctx)
		_, got = svc.Call(ctx, 5, 30*time.Millisecond)
		waited = qenv.Now(ctx).Sub(start)
	})
	if err := s.Run(100 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if !errors.Is(got, qenv.ErrTimeout) || applied != 5 || waited < 30*time.Millisecond {
		t.Fatalf("err=%v applied=%d waited=%v", got, applied, waited)
	}
}

// Predicate waits and Signals wake on the state they wait for.
func TestNativeWaits(t *testing.T) {
	s := qenv.NewNative(qenv.Nominal{}, nil)
	var sig qenv.Signal
	ready, woke, timedOut := false, false, true
	s.Spawn("waiter", func(ctx context.Context) {
		qenv.Wait(ctx, func() bool { return ready })
		woke = true
	})
	s.Spawn("signalled", func(ctx context.Context) {
		timedOut = !sig.Wait(ctx, qenv.Now(ctx).Add(time.Second))
	})
	s.Spawn("setter", func(ctx context.Context) {
		qenv.Sleep(ctx, 10*time.Millisecond)
		ready = true
		sig.Notify()
	})
	if err := s.Run(100 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if !woke || timedOut {
		t.Fatalf("predicate waiter woke=%v; signal timed out=%v", woke, timedOut)
	}
}

// A task crashing its own process stops in Crash.
func TestNativeSelfCrash(t *testing.T) {
	s := qenv.NewNative(qenv.Nominal{}, nil)
	after := false
	s.Process("suicidal", false, func(ctx context.Context) {
		qenv.ProcessOf(ctx).Crash()
		after = true
	})
	if err := s.Run(20 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if after {
		t.Fatal("the task ran on after crashing its own process")
	}
}

// ContextOf is the context a chooser carries: where a run elsewhere
// records.
func TestContextOf(t *testing.T) {
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, 1)
	if qenv.ContextOf(qenv.WithContext(qenv.Nominal{}, ctx)).Value(key{}) != 1 {
		t.Fatal("WithContext's context is lost")
	}
	if qenv.ContextOf(qenv.Nominal{}) == nil {
		t.Fatal("no context for a plain chooser")
	}
}

// Entropy reaches through the wrappers: a Recorder records each chunk and
// Replay hands the same chunks back, between the ordinary choices.
func TestEntropyRecordsAndReplays(t *testing.T) {
	rec := &qenv.ChoiceLog{C: qenv.NewSeeded(5)}
	c := qenv.WithContext(rec, context.Background())
	e := qenv.EntropyOf(c)
	if e == nil {
		t.Fatal("no entropy through WithContext and Recorder")
	}
	a := e(8)
	k := c.Choose("fault.x", 2)
	b := e(8)
	r := qenv.Replay(rec.Choices)
	re := qenv.EntropyOf(r)
	if !reflect.DeepEqual(re(8), a) || r.Choose("fault.x", 2) != k || !reflect.DeepEqual(re(8), b) {
		t.Fatalf("replay differs from %v", rec.Choices)
	}
	if qenv.EntropyOf(qenv.Nominal{}) != nil {
		t.Fatal("Nominal has no entropy")
	}
}
