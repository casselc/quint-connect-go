package queue

import (
	"context"
	"time"
)

// Worker leases jobs from a Service, performs them, and acks them, until
// ctx is done.
type Worker struct {
	Name string
	Svc  *Service
	// Perform does the job's side effect. Default: nothing.
	Perform func(ctx context.Context, job int)
	// Idle is how long to wait when no job is pending (default 5ms).
	Idle time.Duration
}

// Run is the worker loop.
func (w *Worker) Run(ctx context.Context) {
	idle := w.Idle
	if idle == 0 {
		idle = 5 * time.Millisecond
	}
	for ctx.Err() == nil {
		job, ok := w.Svc.Lease(ctx, w.Name)
		if !ok {
			select {
			case <-ctx.Done():
			case <-time.After(idle):
			}
			continue
		}
		w.Once(ctx, job)
	}
}

// Once performs and acks one leased job. A stale ack is normal for an
// at-least-once queue: the job will be (or was) performed again.
func (w *Worker) Once(ctx context.Context, job int) error {
	if w.Perform != nil {
		w.Perform(ctx, job)
	}
	w.Svc.Performed(ctx, w.Name, job)
	return w.Svc.Ack(ctx, w.Name, job)
}
