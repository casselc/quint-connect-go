package jobqueue

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/casselc/quint-connect-go/examples/jobqueue/queue"
	"github.com/casselc/quint-connect-go/qobs"
	"github.com/casselc/quint-connect-go/qtest"
	"github.com/casselc/quint-connect-go/quinthegel"
	"hegel.dev/go/hegel"
)

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

// A worker holding two leases of one job: its first expired and it leased
// the job again. It performs under both; the second ack is stale. Hegel
// found this; the model first recorded leases as a set, lost the second
// lease, and rejected the second perform. The run is a behaviour of the
// design (at least once), and now of the model.
func TestTwoLeasesOfOneJob(t *testing.T) {
	qtest.RequireQuint(t)
	qtest.Record(t, "jobqueue.binding.yaml", func(*qobs.Recorder) {
		svc, c := newService(queue.Faithful)
		ctx := context.Background()
		w1 := &queue.Worker{Name: "w1", Svc: svc}
		_ = svc.Enqueue(ctx, 3)
		j, _ := svc.Lease(ctx, "w1")
		c.advance(2 * ttl)
		k, _ := svc.Lease(ctx, "w1") // the same job, again
		_ = w1.Once(ctx, k)          // completes it
		_ = w1.Once(ctx, j)          // under the expired lease: performed again, stale ack
	}).Expect(t, qtest.Conforms())
}

// The scenario is a behaviour of the model, and the design's guarantee
// (doneImpliesPerformed) holds. Job 1 was performed twice: the model's
// exactlyOnce, which the design does not promise, fails on this run, at
// the second perform. That is a design-level verdict, not a bug in the
// code: the run conforms.
func TestSlowWorkerConformsButRunsTwice(t *testing.T) {
	qtest.RequireQuint(t)
	run := qtest.Record(t, "jobqueue.binding.yaml", func(*qobs.Recorder) { slowWorker(newService(queue.Faithful)) })
	run.Expect(t, qtest.Conforms())

	once := qtest.Validate(t, "jobqueue.binding.yaml", run.Steps, qtest.Invariants("exactlyOnce"))
	once.Expect(t, qtest.Violates("exactlyOnce"))
	rep := once.Report
	if inv := rep.Actors[0].Invariants[0]; inv.Failure.Step.Action != "perform" {
		t.Fatalf("want exactlyOnce violated at a perform:\n%s", rep.Summary())
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
	qtest.RequireQuint(t)
	run := qtest.Record(t, "jobqueue.binding.yaml", func(*qobs.Recorder) {
		svc, _ := newService(queue.Faithful)
		restartScenario(svc)
	})
	run.Expect(t, qtest.Conforms())
	if p := run.Report.Actors[0]; len(p.Calls) == 0 || p.Calls[6].Action != "restart" {
		t.Fatalf("want the restart inferred from the change of incarnation:\n%s", run.Report.Summary())
	}
}

// Both mutants: the same scenarios, rejected at the step the bug makes.
func TestMutantsRejected(t *testing.T) {
	qtest.RequireQuint(t)
	cases := []struct {
		m      queue.Mutation
		run    func(*queue.Service, *clock)
		action string
	}{
		{queue.AckAnyLease, slowWorker, "ack"},                                               // w1's stale ack accepted
		{queue.ForgetDone, func(s *queue.Service, _ *clock) { restartScenario(s) }, "lease"}, // job 1 leased again after the restart
	}
	for _, tc := range cases {
		t.Run(tc.action, func(t *testing.T) {
			run := qtest.Record(t, "jobqueue.binding.yaml", func(*qobs.Recorder) { tc.run(newService(tc.m)) })
			run.Expect(t, qtest.Rejects(tc.action))
		})
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
	qtest.RequireQuint(t)
	quinthegel.Test(t, quinthegel.Config{Binding: "jobqueue.binding.yaml", Mode: quinthegel.After, Opts: []qtest.Option{qtest.Global()}, AllowEmpty: true},
		func(ht *hegel.T, _ *quinthegel.Recorder) { runMachine(ht, queue.Faithful) },
		hegel.WithTestCases(40), hegel.WithDatabase(""))
}

// Against AckAnyLease, checked per case: Hegel shrinks to the shortest
// run in which a worker's ack is accepted after its lease was lost: taken
// over after it expired, or lost in a restart.
func TestHegelShrinksAckAnyLease(t *testing.T) {
	qtest.RequireQuint(t)
	if testing.Short() {
		t.Skip("shrinking against the model runs quint dozens of times")
	}
	v := quinthegel.FindViolation(t, quinthegel.Config{Binding: "jobqueue.binding.yaml", Opts: []qtest.Option{qtest.Global()}}, func(tc hegel.TestCase, _ *quinthegel.Recorder) {
		runMachine(tc, queue.AckAnyLease)
	}, hegel.WithTestCases(60), hegel.WithSeed(5))
	if last := v.Last(); last.Action != "ack" || last.Outcome != "ok" {
		t.Fatalf("want the run to end in an accepted ack:\n%s", quinthegel.Render(v.Steps))
	}
}
