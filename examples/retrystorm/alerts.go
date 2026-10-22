package retrystorm

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"

	"github.com/casselc/quint-connect-go/examples/retrystorm/retry"
	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qload"
	"github.com/casselc/quint-connect-go/qmeta"
	"github.com/casselc/quint-connect-go/qrole"
)

// The application of the alert tier: a server and clients that retry with
// the library's policy, instrumented with qrole where the annotations
// //quint:serve, //quint:operation and //quint:call would weave the same
// calls (qrole/orchestrion.yml). Under load in a simulation, qrole's
// gauges are the chain's u and v; the chain fitted to them gives the
// alerting rules, which then fire on the same gauges.

// ErrRejected is a request refused by a full server queue.
var ErrRejected = errors.New("server queue full")

// Server serves one request at a time at ServiceRate, with at most
// QueueCap waiting or in service; it serves a request whose client has
// given up (wasted work, what sustains a storm).
type Server struct {
	Roles *qrole.Roles
	rnd   *rand.Rand
	busy  bool
	free  qenv.Signal
}

// Handle serves one request (//quint:serve name:server).
func (s *Server) Handle(ctx context.Context, _ int) (_ int, err error) {
	done := qrole.Serve(qrole.WithRoles(ctx, s.Roles), "server")
	defer func() { done(err) }()
	if s.Roles.Queue("server") > QueueCap {
		return 0, ErrRejected
	}
	for s.busy {
		s.free.Wait(ctx, time.Time{})
	}
	s.busy = true
	qenv.Sleep(ctx, time.Duration(s.rnd.ExpFloat64()/ServiceRate*float64(time.Second)))
	s.busy = false
	s.free.Notify()
	return 0, nil
}

// Client makes requests through svc with a retry policy.
type Client struct {
	Svc    *qenv.Service[int, int]
	Policy *retry.Policy
}

// Do makes one request, retrying as the policy says
// (//quint:operation service:server).
func (c *Client) Do(ctx context.Context) (err error) {
	done := qrole.Operation(ctx, "server")
	defer func() { done(err) }()
	c.Policy.Request(qenv.Now(ctx))
	for n := 1; ; n++ {
		if err = c.attempt(ctx); err == nil {
			return nil
		}
		wait, ok := c.Policy.Retry(n, qenv.Now(ctx))
		if !ok {
			return err
		}
		qenv.Sleep(ctx, wait)
	}
}

// attempt is one call (//quint:call service:server).
func (c *Client) attempt(ctx context.Context) (err error) {
	done := qrole.Call(ctx, "server")
	defer func() { done(err) }()
	_, err = c.Svc.Call(ctx, 0, Timeout)
	return err
}

// Instrumented is the application under load at lambda requests per
// second, its server's queue and orbit probed as "u" and "v" (qrole's
// gauges).
func (p Policy) Instrumented(lambda float64) qload.Experiment {
	return qload.Experiment{
		Rate: lambda, Healthy: lambda,
		Deploy: func(env *qload.Env) func(ctx context.Context, i int) {
			roles := qrole.New()
			srv := &Server{Roles: roles, rnd: env.Rand("service")}
			svc := qenv.NewService(env.Sim, "server", nil, srv.Handle)
			client := &Client{Svc: svc, Policy: p.New(env.Rand("backoff"), retry.Faithful)}
			env.Probe("u", func() int { return roles.Queue("server") })
			env.Probe("v", func() int { return roles.Orbit("server") })
			return func(ctx context.Context, i int) {
				_ = client.Do(qrole.WithRoles(ctx, roles))
			}
		},
	}
}

// Observations are a run's probed states, for qmeta.Trajectories.
func Observations(s *qload.Series) []qmeta.Observation {
	var out []qmeta.Observation
	for _, pt := range s.Points {
		out = append(out, qmeta.Observation{T: pt.T.Seconds(), U: pt.Probes["u"], V: pt.Probes["v"]})
	}
	return out
}
