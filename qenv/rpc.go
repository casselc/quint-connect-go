package qenv

import (
	"context"
	"errors"
	"time"
)

// ErrTimeout is returned by Call when no response arrived in time. The
// request may or may not have been processed: the caller cannot tell.
var ErrTimeout = errors.New("qenv: call timed out (outcome unknown)")

// ErrOverloaded is returned by Call when the service's queue was full: the
// request was not processed.
var ErrOverloaded = errors.New("qenv: service overloaded (request rejected)")

// Capacity makes a service a queued server, for load and metastability
// experiments (see qload, qmeta). A request waits for one of Workers; at
// most Queue wait (more are rejected with ErrOverloaded). Its service takes
// Time, times Slowdown at that moment, before the handler runs. A request
// whose caller has already given up is still served, as most servers do
// (wasted work, what sustains a retry storm), unless Shed is set.
//
// Time draws from the experiment's own seeded generator, not the Chooser:
// a load run has thousands of requests, and what a search should vary is
// the trigger (Slowdown), not each service time.
type Capacity struct {
	Workers  int // default 1
	Queue    int
	Time     func() time.Duration
	Slowdown func(elapsed time.Duration) float64 // nil: 1
	// Shed drops a request whose caller's deadline passed while it waited:
	// deadline propagation, the server-side mitigation.
	Shed bool
}

// Service is a remote service: a handler run in its own process, reached
// over a network whose contract is "at most a response, maybe late, maybe
// never". Its faults:
//   - "fault.rpc.<name>.request": 1 loses the request (never processed),
//     2 duplicates it (processed twice, as a retransmission can);
//   - "fault.rpc.<name>.response": 1 loses the response after the request
//     was processed, the ambiguous failure that makes retries dangerous.
//
// A request to a crashed process is lost.
type Service[Req, Resp any] struct {
	Name    string
	Proc    *Process
	handler func(ctx context.Context, req Req) (Resp, error)
	sim     *Sim
	// the service's fault kinds: a request dropped or delivered twice, a
	// response lost
	request, response FaultKind
	// Calls counts handler invocations.
	Calls int
	// Capacity, if set, queues requests for workers (set before the run).
	Capacity *Capacity
	// CrashWindow, if set, lets a crash of the service's process fall
	// between the handler's return and the response's delivery: the effect
	// committed, the caller never told, as when a server dies after its
	// commit. Without it the response leaves in the handler's scheduling
	// step, and only the "lost" response fault stands in for that crash.
	// It adds a scheduling point per call, so set it for new tests: it
	// changes the choices a recorded run replays.
	CrashWindow bool
	// Rejected and Shed count requests refused by a full queue, and dropped
	// because their caller had given up.
	Rejected, Shed int

	busy, waiting int
	free          Signal
}

// InSystem is the number of requests waiting or in service (with Capacity).
func (svc *Service[Req, Resp]) InSystem() int { return svc.busy + svc.waiting }

// serve runs the queueing of a request with Capacity: false if it was
// rejected (rejected true) or shed.
func (svc *Service[Req, Resp]) serve(ctx context.Context, deadline time.Time) (ok, rejected bool) {
	c := svc.Capacity
	workers := max(c.Workers, 1)
	if svc.busy >= workers {
		if svc.waiting >= c.Queue {
			svc.Rejected++
			return false, true
		}
		svc.waiting++
		for svc.busy >= workers {
			svc.free.Wait(ctx, time.Time{})
		}
		svc.waiting--
	}
	if c.Shed && svc.sim.Now().After(deadline) {
		svc.Shed++
		svc.free.Notify()
		return false, false
	}
	svc.busy++
	d := c.Time()
	if c.Slowdown != nil {
		d = time.Duration(float64(d) * c.Slowdown(svc.sim.Elapsed()))
	}
	Sleep(ctx, d)
	return true, false
}

func (svc *Service[Req, Resp]) release() {
	svc.busy--
	svc.free.Notify()
}

// NewService registers a service whose handler runs in process p (nil: a
// process of its own, which never crashes).
func NewService[Req, Resp any](s *Sim, name string, p *Process, handler func(ctx context.Context, req Req) (Resp, error)) *Service[Req, Resp] {
	if p == nil {
		p = s.Process(name, false, func(context.Context) {})
	}
	return &Service[Req, Resp]{Name: name, Proc: p, handler: handler, sim: s,
		request:  Faults("rpc."+name+".request", "drop", "duplicate"),
		response: Faults("rpc."+name+".response", "lost")}
}

type result[Resp any] struct {
	resp Resp
	err  error
	done bool
}

// Call sends req and waits up to timeout of virtual time for the response.
func (svc *Service[Req, Resp]) Call(ctx context.Context, req Req, timeout time.Duration) (Resp, error) {
	Yield(ctx)
	s := svc.sim
	res := &result[Resp]{}
	deliveries := 1
	switch s.FaultOf(svc.request) {
	case "drop":
		deliveries = 0
	case "duplicate":
		deliveries = 2
	}
	if svc.Proc.Down() {
		deliveries = 0
	}
	deadline := s.Now().Add(timeout)
	for i := 0; i < deliveries; i++ {
		first := i == 0
		s.spawn(s.base, svc.Name+".handle", svc.Proc, func(hctx context.Context) {
			if svc.Capacity != nil {
				ok, rejected := svc.serve(hctx, deadline)
				if rejected && first {
					res.err, res.done = ErrOverloaded, true
				}
				if !ok {
					return
				}
				defer svc.release()
			}
			svc.Calls++
			resp, err := svc.handler(hctx, req)
			if svc.CrashWindow {
				Yield(hctx) // a crash here kills the task: no response
			}
			if !first {
				return // the duplicate's response is dropped as a stray
			}
			if s.FaultOf(svc.response) == "lost" {
				return
			}
			res.resp, res.err, res.done = resp, err, true
		})
	}
	var zero Resp
	if !WaitUntil(ctx, func() bool { return res.done }, deadline) {
		return zero, ErrTimeout
	}
	return res.resp, res.err
}
