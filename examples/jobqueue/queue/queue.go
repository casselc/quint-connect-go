// Package queue is the jobqueue example app: an at-least-once job queue
// with leases, and workers that perform jobs.
//
// A Service owns the current queue incarnation and its journal. Restart
// simulates a crash of the queue process: the new incarnation recovers
// the jobs, and which are done, from the journal; leases, held in memory,
// are lost. Workers (worker.go) lease jobs, perform them, and ack them.
//
// Every model step of model/jobqueue.qnt is recorded through qobs from
// inside the queue's critical section, with the incarnation as
// quint.process, so the binding's restart action is inferred where it
// changes.
package queue

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/casselc/quint-connect-go/qobs"
)

// ErrStaleLease: the ack's lease expired, or was lost in a restart; the job
// is someone else's, or pending again. It classifies itself for the model.
var ErrStaleLease = staleErr{}

type staleErr struct{}

func (staleErr) Error() string        { return "stale lease" }
func (staleErr) QuintOutcome() string { return "stale" }

// ErrDuplicate: a job with this id was already enqueued.
var ErrDuplicate = errors.New("duplicate job")

// Mutation injects a bug.
type Mutation int

const (
	Faithful Mutation = iota
	// AckAnyLease accepts an ack from a worker whose lease expired and was
	// taken over: the job is marked done while another worker holds it.
	AckAnyLease
	// ForgetDone does not journal acks, so after a restart done jobs are
	// pending again and are leased (and performed) again.
	ForgetDone
)

type status int

const (
	pending status = iota
	leased
	done
)

type job struct {
	id     int
	status status
	holder string
	until  time.Time
}

// Journal is the queue's durable state: the jobs enqueued, in order, and
// those done.
type Journal struct {
	mu   sync.Mutex
	jobs []int
	done map[int]bool
}

// Service is the queue as clients see it, across restarts.
type Service struct {
	mu       sync.Mutex
	q        *queue
	journal  *Journal
	epoch    int
	ttl      time.Duration
	now      func() time.Time
	mutation Mutation
}

// Config of a Service.
type Config struct {
	LeaseTTL time.Duration    // default 1s
	Now      func() time.Time // default time.Now
	Mutation Mutation
}

// New starts a service with an empty journal.
func New(c Config) *Service {
	if c.LeaseTTL == 0 {
		c.LeaseTTL = time.Second
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	s := &Service{journal: &Journal{done: map[int]bool{}}, ttl: c.LeaseTTL, now: c.Now, mutation: c.Mutation}
	s.start()
	return s
}

func (s *Service) start() {
	s.epoch++
	q := &queue{svc: s, epoch: fmt.Sprintf("queue-%d", s.epoch), jobs: map[int]*job{}}
	s.journal.mu.Lock()
	for _, id := range s.journal.jobs {
		st := pending
		if s.journal.done[id] {
			st = done
		}
		q.jobs[id] = &job{id: id, status: st}
		q.order = append(q.order, id)
	}
	s.journal.mu.Unlock()
	s.q = q
}

// Restart replaces the queue incarnation, as after a crash of the queue
// process: leases are lost, the journal survives.
func (s *Service) Restart() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.q.mu.Lock() // the old incarnation is gone: nothing more is recorded under it
	s.q.dead = true
	s.q.mu.Unlock()
	s.start()
}

func (s *Service) current() *queue { s.mu.Lock(); defer s.mu.Unlock(); return s.q }

// lock returns the current incarnation, locked. An operation that raced
// with Restart retries on the new incarnation, so no step is recorded
// under a dead one.
func (s *Service) lock() *queue {
	for {
		q := s.current()
		q.mu.Lock()
		if !q.dead {
			return q
		}
		q.mu.Unlock()
	}
}

// queue is one incarnation.
type queue struct {
	mu    sync.Mutex
	svc   *Service
	epoch string
	jobs  map[int]*job
	order []int
	dead  bool
}

func (q *queue) record(ctx context.Context, action string, err error, kv ...any) {
	qobs.Record(ctx, action, err, append(kv, "actor", "jobqueue", "process", q.epoch)...)
}

// Enqueue adds a job.
func (s *Service) Enqueue(ctx context.Context, id int) error {
	q := s.lock()
	defer q.mu.Unlock()
	if _, ok := q.jobs[id]; ok {
		return ErrDuplicate
	}
	s.journal.mu.Lock()
	s.journal.jobs = append(s.journal.jobs, id)
	s.journal.mu.Unlock()
	q.jobs[id] = &job{id: id, status: pending}
	q.order = append(q.order, id)
	q.record(ctx, "enqueue", nil, "job", id)
	return nil
}

// expire returns jobs whose lease ran out to pending; with q.mu held.
func (q *queue) expire(ctx context.Context) {
	now := q.svc.now()
	for _, id := range q.order {
		j := q.jobs[id]
		if j.status == leased && !now.Before(j.until) {
			j.status, j.holder = pending, ""
			q.record(ctx, "expire", nil, "job", id)
		}
	}
}

// Lease gives worker the oldest pending job. ok is false when none is pending.
func (s *Service) Lease(ctx context.Context, worker string) (id int, ok bool) {
	q := s.lock()
	defer q.mu.Unlock()
	q.expire(ctx)
	for _, id := range q.order {
		j := q.jobs[id]
		if j.status == pending {
			j.status, j.holder, j.until = leased, worker, s.now().Add(s.ttl)
			q.record(ctx, "lease", nil, "worker", worker, "job", id)
			return id, true
		}
	}
	return 0, false
}

// Expire returns timed-out leases to pending (Lease does it too).
func (s *Service) Expire(ctx context.Context) {
	q := s.lock()
	defer q.mu.Unlock()
	q.expire(ctx)
}

// Performed reports that worker performed job: the side effect happened.
// Workers report through the queue so the step is ordered with the
// queue's own steps and carries the current incarnation.
func (s *Service) Performed(ctx context.Context, worker string, id int) {
	q := s.lock()
	defer q.mu.Unlock()
	q.record(ctx, "perform", nil, "worker", worker, "job", id)
}

// Ack completes job if worker still holds its lease.
func (s *Service) Ack(ctx context.Context, worker string, id int) (err error) {
	q := s.lock()
	defer q.mu.Unlock()
	defer func() { q.record(ctx, "ack", err, "worker", worker, "job", id) }()
	q.expire(ctx)
	j, ok := q.jobs[id]
	if !ok || j.status == done || (j.holder != worker && s.mutation != AckAnyLease) || (j.status != leased && s.mutation != AckAnyLease) {
		return ErrStaleLease
	}
	j.status, j.holder = done, ""
	if s.mutation != ForgetDone {
		s.journal.mu.Lock()
		s.journal.done[id] = true
		s.journal.mu.Unlock()
	}
	return nil
}

// Status lists the jobs: id -> "pending", "leased:<worker>" or "done".
func (s *Service) Status() map[int]string {
	q := s.lock()
	defer q.mu.Unlock()
	out := map[int]string{}
	ids := append([]int(nil), q.order...)
	sort.Ints(ids)
	for _, id := range ids {
		j := q.jobs[id]
		switch j.status {
		case pending:
			out[id] = "pending"
		case leased:
			out[id] = "leased:" + j.holder
		case done:
			out[id] = "done"
		}
	}
	return out
}
