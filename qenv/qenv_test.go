package qenv_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/casselc/quint-connect-go/qenv"
	"hegel.dev/go/hegel"
)

// Two read-modify-write increments of one counter, run concurrently.
func increments(s *qenv.Sim, st *qenv.Store, forUpdate bool) {
	for i := 0; i < 2; i++ {
		s.Spawn(fmt.Sprintf("inc%d", i), func(ctx context.Context) {
			_ = qenv.Retry(ctx, st, 5, func(tx qenv.Txn) error {
				var v any
				var err error
				if forUpdate {
					v, _, err = tx.GetForUpdate("n")
				} else {
					v, _ = tx.Get("n")
				}
				if err != nil {
					return err
				}
				n, _ := v.(int)
				tx.Put("n", n+1)
				return nil
			})
		})
	}
}

// Two on-call doctors each go off call if the other is still on: the
// classic write skew, which leaves nobody on call.
func offCall(s *qenv.Sim, st *qenv.Store) {
	for _, me := range []string{"alice", "bob"} {
		other := map[string]string{"alice": "bob", "bob": "alice"}[me]
		s.Spawn(me, func(ctx context.Context) {
			_ = qenv.Retry(ctx, st, 5, func(tx qenv.Txn) error {
				if v, _ := tx.Get("oncall/" + other); v == true {
					tx.Put("oncall/"+me, false)
				}
				return nil
			})
		})
	}
}

func run(t *testing.T, c qenv.Chooser, iso qenv.Isolation, setup func(*qenv.Sim, *qenv.Store)) *qenv.Store {
	t.Helper()
	s := qenv.New(c)
	defer s.Close()
	st := qenv.NewStore(s, "db", iso)
	st.Load("oncall/alice", true)
	st.Load("oncall/bob", true)
	setup(s, st)
	if err := s.Run(time.Minute); err != nil {
		t.Fatal(err)
	}
	return st
}

// anomaly counts the seeds (of 200) whose run shows the anomaly.
func anomaly(t *testing.T, iso qenv.Isolation, setup func(*qenv.Sim, *qenv.Store), bad func(*qenv.Store) bool) int {
	n := 0
	for seed := uint64(0); seed < 200; seed++ {
		if bad(run(t, qenv.NewSeeded(seed), iso, setup)) {
			n++
		}
	}
	return n
}

func lostUpdate(st *qenv.Store) bool { v, _ := st.Read("n"); return v != 2 }
func nobodyOnCall(st *qenv.Store) bool {
	a, _ := st.Read("oncall/alice")
	b, _ := st.Read("oncall/bob")
	return a == false && b == false
}

// Each isolation level allows exactly its anomalies.
func TestIsolationAnomalies(t *testing.T) {
	plain := func(s *qenv.Sim, st *qenv.Store) { increments(s, st, false) }
	locked := func(s *qenv.Sim, st *qenv.Store) { increments(s, st, true) }
	cases := []struct {
		name  string
		iso   qenv.Isolation
		setup func(*qenv.Sim, *qenv.Store)
		bad   func(*qenv.Store) bool
		want  bool
	}{
		{"lost update, read committed", qenv.ReadCommitted, plain, lostUpdate, true},
		{"lost update, read committed, FOR UPDATE", qenv.ReadCommitted, locked, lostUpdate, false},
		{"lost update, snapshot isolation", qenv.SnapshotIsolation, plain, lostUpdate, false},
		{"write skew, snapshot isolation", qenv.SnapshotIsolation, offCall, nobodyOnCall, true},
		{"write skew, read committed", qenv.ReadCommitted, offCall, nobodyOnCall, true},
		{"write skew, serializable", qenv.Serializable, offCall, nobodyOnCall, false},
		{"lost update, serializable", qenv.Serializable, plain, lostUpdate, false},
	}
	for _, c := range cases {
		n := anomaly(t, c.iso, c.setup, c.bad)
		if (n > 0) != c.want {
			t.Errorf("%s: anomaly in %d of 200 seeded runs, want any: %v", c.name, n, c.want)
		}
	}
}

// The same seed gives the same run; Replay of its choices gives it again.
func TestDeterministicAndReplayable(t *testing.T) {
	var runs [][]qenv.Choice
	for i := 0; i < 2; i++ {
		rec := &qenv.Recorder{C: qenv.NewSeeded(42)}
		run(t, rec, qenv.ReadCommitted, func(s *qenv.Sim, st *qenv.Store) { increments(s, st, false) })
		runs = append(runs, rec.Choices)
	}
	if fmt.Sprint(runs[0]) != fmt.Sprint(runs[1]) {
		t.Fatalf("same seed, different choices:\n%v\n%v", runs[0], runs[1])
	}
	rec := &qenv.Recorder{C: qenv.Replay(runs[0])}
	run(t, rec, qenv.ReadCommitted, func(s *qenv.Sim, st *qenv.Store) { increments(s, st, false) })
	if fmt.Sprint(rec.Choices) != fmt.Sprint(runs[0]) {
		t.Fatalf("replay differs:\n%v\n%v", rec.Choices, runs[0])
	}
}

// A crash aborts the process's open transaction (releasing its lock) and
// redelivers the message it held; the restarted incarnation finishes.
func TestCrashReleasesAndRedelivers(t *testing.T) {
	s := qenv.New(qenv.Nominal{})
	defer s.Close()
	st := qenv.NewStore(s, "db", qenv.ReadCommitted)
	q := qenv.NewQueue(s, "jobs")
	crashNext := true
	var p *qenv.Process
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
				p.Restart()
				qenv.Yield(ctx) // the crashed task never gets past here
				t.Error("a crashed task ran on")
			}
			if err := tx.Commit(); err != nil {
				t.Error(err)
			}
			d.Ack(ctx)
		}
	})
	s.Spawn("client", func(ctx context.Context) { q.Publish(ctx, "j1") })
	if err := s.Run(time.Minute); err != nil {
		t.Fatal(err)
	}
	if v, _ := st.Read("job"); v != "j1" || q.Redelivered != 1 || p.Incarnation != 2 {
		t.Fatalf("job=%v redelivered=%d incarnation=%d\n%s", v, q.Redelivered, p.Incarnation, s.Tasks())
	}
}

// A lost response is a timeout after the request took effect.
func TestLostResponseIsAmbiguous(t *testing.T) {
	applied := 0
	var got error
	h := &forced{faults: map[string]int{"fault.rpc.pay.response": 1}}
	s := qenv.New(h)
	defer s.Close()
	svc := qenv.NewService(s, "pay", nil, func(ctx context.Context, n int) (int, error) { applied += n; return applied, nil })
	s.Spawn("client", func(ctx context.Context) {
		_, got = svc.Call(ctx, 5, time.Second)
	})
	if err := s.Run(time.Minute); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(got, qenv.ErrTimeout) || applied != 5 {
		t.Fatalf("err=%v applied=%d", got, applied)
	}
	if s.Elapsed() != time.Second {
		t.Fatalf("virtual time %v, want the timeout", s.Elapsed())
	}
}

type forced struct{ faults map[string]int }

func (f *forced) Choose(kind string, n int) int { return f.faults[kind] }

// Hegel finds the lost update under read committed and shrinks the
// schedule; the faithful (FOR UPDATE) version passes.
func TestHegelFindsLostUpdate(t *testing.T) {
	check := func(forUpdate bool) error {
		return hegel.Run(func(tc hegel.TestCase) {
			st := run(t, qenv.Hegel(tc), qenv.ReadCommitted, func(s *qenv.Sim, st *qenv.Store) { increments(s, st, forUpdate) })
			if lostUpdate(st) {
				tc.Errorf("lost update")
			}
		}, hegel.WithTestCases(100), hegel.WithDatabase(""))
	}
	if err := check(false); err == nil {
		t.Fatalf("want a lost update, got %v", err)
	}
	if err := check(true); err != nil {
		t.Fatal(err)
	}
}

// A timestamp taken from the real clock, translated to virtual time, ages
// with the simulation: time.Since of its translation back measures virtual
// time.
func TestClockTranslation(t *testing.T) {
	s := qenv.New(qenv.Nominal{})
	defer s.Close()
	stored := s.ToVirtual(time.Now())
	s.Spawn("sleeper", func(ctx context.Context) { qenv.Sleep(ctx, 45*time.Second) })
	if err := s.Run(time.Minute); err != nil {
		t.Fatal(err)
	}
	if age := time.Since(s.ToReal(stored)); age < 44*time.Second || age > 46*time.Second {
		t.Fatalf("aged %v in real time, want the 45s of virtual time", age)
	}
}

// Two tasks wait for the same lock: when it is released, only the first to
// run gets it; the other's predicate is false again, and it waits on.
func TestPredicateWaitersRecheck(t *testing.T) {
	s := qenv.New(qenv.Nominal{})
	defer s.Close()
	held := true
	inside := 0
	for i := 0; i < 2; i++ {
		s.Spawn(fmt.Sprintf("waiter%d", i), func(ctx context.Context) {
			qenv.Wait(ctx, func() bool { return !held })
			held = true
			inside++
			if inside > 1 {
				t.Error("both waiters took the lock")
			}
		})
	}
	s.Spawn("releaser", func(ctx context.Context) {
		qenv.Sleep(ctx, time.Second)
		held = false
	})
	if err := s.Run(time.Minute); err != nil {
		t.Fatal(err)
	}
	if inside != 1 {
		t.Fatalf("%d waiters got the lock, want 1", inside)
	}
}

// Many waiting tasks cost the scheduler nothing per step: 20000 requests,
// each waiting on a Signal with a timeout, finish in well under a second.
func TestManyWaitersScale(t *testing.T) {
	s := qenv.New(qenv.Nominal{})
	defer s.Close()
	sigs := make([]*qenv.Signal, 20000)
	answered := 0
	for i := range sigs {
		sigs[i] = &qenv.Signal{}
		s.Spawn("request", func(ctx context.Context) {
			if sigs[i].Wait(ctx, qenv.Now(ctx).Add(time.Hour)) {
				answered++
			}
		})
	}
	s.Spawn("server", func(ctx context.Context) {
		for _, g := range sigs {
			qenv.Sleep(ctx, time.Millisecond)
			g.Notify()
		}
	})
	t0 := time.Now()
	s.MaxSteps = 1 << 30
	if err := s.Run(time.Hour); err != nil {
		t.Fatal(err)
	}
	if answered != len(sigs) {
		t.Fatalf("%d answered", answered)
	}
	if d := time.Since(t0); d > 30*time.Second { // ~0.2 s; generous for -race
		t.Fatalf("took %v", d)
	}
	t.Logf("20000 waiting requests: %v", time.Since(t0))
}

// A Signal wait that timed out leaves no trace: a later Notify does not
// cut short the task's next wait.
func TestSignalTimeoutIsForgotten(t *testing.T) {
	s := qenv.New(qenv.Nominal{})
	defer s.Close()
	var sig qenv.Signal
	var slept time.Duration
	s.Spawn("waiter", func(ctx context.Context) {
		if sig.Wait(ctx, s.Now().Add(time.Second)) {
			t.Error("woken before any Notify")
		}
		start := s.Elapsed()
		qenv.Sleep(ctx, 10*time.Second)
		slept = s.Elapsed() - start
	})
	s.Spawn("notifier", func(ctx context.Context) {
		qenv.Sleep(ctx, 5*time.Second)
		sig.Notify()
	})
	if err := s.Run(time.Minute); err != nil {
		t.Fatal(err)
	}
	if slept != 10*time.Second {
		t.Errorf("slept %v, want 10s: a stale Signal entry woke the sleep", slept)
	}
}
