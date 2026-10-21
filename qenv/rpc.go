package qenv

import (
	"context"
	"errors"
	"time"
)

// ErrTimeout is returned by Call when no response arrived in time. The
// request may or may not have been processed: the caller cannot tell.
var ErrTimeout = errors.New("qenv: call timed out (outcome unknown)")

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
	// Calls counts handler invocations.
	Calls int
}

// NewService registers a service whose handler runs in process p (nil: a
// process of its own, which never crashes).
func NewService[Req, Resp any](s *Sim, name string, p *Process, handler func(ctx context.Context, req Req) (Resp, error)) *Service[Req, Resp] {
	if p == nil {
		p = s.Process(name, false, func(context.Context) {})
	}
	return &Service[Req, Resp]{Name: name, Proc: p, handler: handler, sim: s}
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
	switch s.Fault("rpc."+svc.Name+".request", 3) {
	case 1:
		deliveries = 0
	case 2:
		deliveries = 2
	}
	if svc.Proc.Down() {
		deliveries = 0
	}
	for i := 0; i < deliveries; i++ {
		first := i == 0
		s.spawn(context.Background(), svc.Name+".handle", svc.Proc, func(hctx context.Context) {
			svc.Calls++
			resp, err := svc.handler(hctx, req)
			if !first {
				return // the duplicate's response is dropped as a stray
			}
			if s.Fault("rpc."+svc.Name+".response", 2) == 1 {
				return
			}
			res.resp, res.err, res.done = resp, err, true
		})
	}
	var zero Resp
	if !WaitUntil(ctx, func() bool { return res.done }, s.now.Add(timeout)) {
		return zero, ErrTimeout
	}
	return res.resp, res.err
}
