package jobqueue

import (
	"context"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/casselc/quint-connect-go/examples/jobqueue/queue"
	"github.com/casselc/quint-connect-go/qobs"
	"github.com/casselc/quint-connect-go/qtest"
	"github.com/casselc/quint-connect-go/qtrace"
	"github.com/casselc/quint-connect-go/quinthegel"
	"github.com/casselc/quint-connect-go/validate"
	"hegel.dev/go/hegel"
)

func needQuint(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("quint"); err != nil {
		t.Skip("quint not installed")
	}
}

const ttl = time.Second

// clock is a manual clock for the queue's leases.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

func newService(m queue.Mutation) (*queue.Service, *clock) {
	c := &clock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	return queue.New(queue.Config{LeaseTTL: ttl, Now: c.now, Mutation: m}), c
}

// slowWorker: w1 leases job 1 and is slow; its lease expires and w2 takes
// the job; both perform it; w1's ack is stale, w2's completes the job.
func slowWorker(svc *queue.Service, c *clock) {
	ctx := context.Background()
	w1 := &queue.Worker{Name: "w1", Svc: svc}
	w2 := &queue.Worker{Name: "w2", Svc: svc}
	_ = svc.Enqueue(ctx, 1)
	_ = svc.Enqueue(ctx, 2)
	j, _ := svc.Lease(ctx, "w1") // job 1
	c.advance(2 * ttl)           // w1 is slow
	k, _ := svc.Lease(ctx, "w2") // job 1 again: its lease expired
	_ = w1.Once(ctx, j)          // stale ack
	_ = w2.Once(ctx, k)
	j, _ = svc.Lease(ctx, "w1") // job 2
	_ = w1.Once(ctx, j)
}

// The scenario is a behaviour of the model, and the design's guarantee
// (doneImpliesPerformed) holds. Job 1 was performed twice: the model's
// exactlyOnce, which the design does not promise, fails on this run, at
// the second perform. That is a design-level verdict, not a bug in the
// code: the run conforms.
func TestSlowWorkerConformsButRunsTwice(t *testing.T) {
	needQuint(t)
	run := qtest.RecordAndCheck(t, "jobqueue.binding.yaml", func(*qobs.Recorder) { slowWorker(newService(queue.Faithful)) })

	c, err := validate.NewChecker("jobqueue.binding.yaml", validate.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	once, err := c.WithInvariants("exactlyOnce")
	if err != nil {
		t.Fatal(err)
	}
	rep, err := once.Check(context.Background(), run.Steps)
	if err != nil {
		t.Fatal(err)
	}
	r := rep.Actors[0]
	inv := r.Invariants[0]
	if r.Conforms != validate.Pass || inv.Verdict != validate.Fail || inv.Failure.Step.Action != "perform" {
		t.Fatalf("want a conforming run violating exactlyOnce at a perform:\n%s", rep.Summary())
	}
	t.Logf("as designed:\n%s", rep.Summary())
}

// A restart loses the leases: an ack after it is stale, and the job is
// leased again from the journal.
func restartScenario(svc *queue.Service) {
	ctx := context.Background()
	w1 := &queue.Worker{Name: "w1", Svc: svc}
	_ = svc.Enqueue(ctx, 1)
	_ = svc.Enqueue(ctx, 2)
	j, _ := svc.Lease(ctx, "w1")
	_ = w1.Once(ctx, j) // job 1 done, journaled
	j, _ = svc.Lease(ctx, "w1")
	svc.Restart()
	_ = w1.Once(ctx, j) // stale: the restart lost the lease
	j, _ = svc.Lease(ctx, "w1")
	_ = w1.Once(ctx, j)
}

func TestRestartConforms(t *testing.T) {
	needQuint(t)
	run := qtest.RecordAndCheck(t, "jobqueue.binding.yaml", func(*qobs.Recorder) {
		svc, _ := newService(queue.Faithful)
		restartScenario(svc)
	})
	if p := run.Report.Actors[0]; len(p.Calls) == 0 || p.Calls[6].Action != "restart" {
		t.Fatalf("want the restart inferred from the change of incarnation:\n%s", run.Report.Summary())
	}
}

// Both mutants: the same scenarios, rejected at the step the bug makes.
func TestMutantsRejected(t *testing.T) {
	needQuint(t)
	cases := []struct {
		m      queue.Mutation
		run    func(*queue.Service, *clock)
		action string
	}{
		{queue.AckAnyLease, slowWorker, "ack"},                                               // w1's stale ack accepted
		{queue.ForgetDone, func(s *queue.Service, _ *clock) { restartScenario(s) }, "lease"}, // job 1 leased again after the restart
	}
	for _, tc := range cases {
		run := qtest.Record(t, "jobqueue.binding.yaml", func(*qobs.Recorder) { tc.run(newService(tc.m)) })
		f := run.Report.Actors[0].Failure
		if run.OK() || f == nil || f.Kind != "disabled" || f.Step.Action != tc.action {
			t.Errorf("mutant %d: want %s rejected:\n%s", tc.m, tc.action, run.Report.Summary())
		}
	}
}

// ---- Hegel ------------------------------------------------------------------

type held struct {
	worker string
	job    int
}

// machine drives the queue with arbitrary interleavings of enqueues,
// leases, slow and fast work, acks, time passing and restarts.
type machine struct {
	svc   *queue.Service
	clock *clock
	held  *hegel.Pool[held] // leases workers believe they hold
}

var workers = hegel.SampledFrom([]string{"w1", "w2"})

func (m *machine) RuleEnqueue(tc hegel.TestCase) {
	_ = m.svc.Enqueue(context.Background(), hegel.Draw(tc, hegel.Integers(1, 4)))
}

func (m *machine) RuleLease(tc hegel.TestCase) {
	w := hegel.Draw(tc, workers)
	if j, ok := m.svc.Lease(context.Background(), w); ok {
		m.held.Add(tc, held{w, j})
	}
}

func (m *machine) RuleWork(tc hegel.TestCase) {
	h := hegel.Draw(tc, m.held.ValuesConsumed())
	_ = (&queue.Worker{Name: h.worker, Svc: m.svc}).Once(context.Background(), h.job)
}

func (m *machine) RuleTick(tc hegel.TestCase) {
	m.clock.advance(time.Duration(hegel.Draw(tc, hegel.Integers(1, 3))) * ttl / 2)
}

func (m *machine) RuleRestart(tc hegel.TestCase) {
	tc.Assume(hegel.Draw(tc, hegel.Integers(0, 3)) == 0) // rarely
	m.svc.Restart()
}

func runMachine(tc hegel.TestCase, mut queue.Mutation) {
	svc, c := newService(mut)
	hegel.RunStateful(tc, &machine{svc: svc, clock: c, held: hegel.NewPool[held](tc)}, hegel.WithStatefulStepCount(14))
}

// Every run Hegel generates is a behaviour of the model (checked after
// the test, in parallel).
func TestHegelRunsConform(t *testing.T) {
	needQuint(t)
	quinthegel.Test(t, quinthegel.Config{Binding: "jobqueue.binding.yaml", Mode: quinthegel.After, Global: true, AllowEmpty: true},
		func(ht *hegel.T, _ *quinthegel.Recorder) { runMachine(ht, queue.Faithful) },
		hegel.WithTestCases(40), hegel.WithDatabase(""))
}

// Against AckAnyLease, checked per case: Hegel shrinks to the shortest
// run in which a worker's ack is accepted after its lease was lost: taken
// over after it expired, or lost in a restart.
func TestHegelShrinksAckAnyLease(t *testing.T) {
	needQuint(t)
	if testing.Short() {
		t.Skip("shrinking against the model runs quint dozens of times")
	}
	c, err := quinthegel.Checker("jobqueue.binding.yaml", validate.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	cc := quinthegel.NewCaseChecker(c)
	var last []qtrace.Step
	err = hegel.Run(func(tc hegel.TestCase) {
		rec := quinthegel.NewRecorder(tc)
		defer rec.Install()()
		runMachine(tc, queue.AckAnyLease)
		last = rec.Steps()
		quinthegel.CheckCase(tc, cc, last)
	}, hegel.SuppressHealthCheck(hegel.TooSlow), hegel.WithTestCases(60), hegel.WithDatabase(""), hegel.WithSeed(5))
	q, hits := cc.Stats()
	t.Logf("%d quint checks, %d from the cache; shrunk run:\n%s", q, hits, quinthegel.Render(last))
	if err == nil {
		t.Fatal("the AckAnyLease mutant passed")
	}
	if n := len(last); n == 0 || last[n-1].Action != "ack" || last[n-1].Outcome != "ok" {
		t.Fatalf("want the run to end in an accepted ack:\n%s", quinthegel.Render(last))
	}
}
