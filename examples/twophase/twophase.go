// Package twophase deploys two-phase commit (package tpc) into a qenv
// simulation, checked against a Choreo model (model/twophase.qnt): a
// coordinator and participants as crashable processes, each with its own
// durable store, talking over a network that loses and duplicates requests
// and loses responses. Participants vote (by a schedule choice, mostly to
// commit) and resend their vote until they hear the decision; the
// coordinator decides to abort if not every vote arrives in time, and
// resends its decision until each participant has it.
package twophase

import (
	"context"
	"fmt"
	"time"

	"github.com/casselc/quint-connect-go/examples/twophase/tpc"
	"github.com/casselc/quint-connect-go/qenv"
)

// Config of a deployment.
type Config struct {
	Participants int // default 3: p1, p2, p3
	Mutation     tpc.Mutation
	NoCrashes    bool
}

// Names are the participants' names, as the model's PARTICIPANTS.
func (c Config) Names() []string {
	n := c.Participants
	if n == 0 {
		n = 3
	}
	var out []string
	for i := 1; i <= n; i++ {
		out = append(out, fmt.Sprintf("p%d", i))
	}
	return out
}

// Constants are the model's for the deployment (qtest.Constants).
func (c Config) Constants() map[string]string {
	s := ""
	for i, n := range c.Names() {
		if i > 0 {
			s += ", "
		}
		s += fmt.Sprintf("%q", n)
	}
	return map[string]string{"PARTICIPANTS": "Set(" + s + ")"}
}

// Timing: how long a coordinator waits for votes, how long a call waits
// for its response, how long a prepared participant waits before it
// gives up (only the impatient mutant does).
const (
	VoteTimeout = 3 * time.Second
	CallTimeout = time.Second
	Patience    = 4 * time.Second
)

type vote struct{ Node string }

// Deployment is a running protocol in a simulation.
type Deployment struct {
	Sim          *qenv.Sim
	Coordinator  *tpc.Coordinator
	Participants map[string]*tpc.Participant
	votes        *qenv.Service[vote, tpc.Stage]
	decide       map[string]*qenv.Service[bool, string]
}

// Deploy starts the coordinator and the participants in s.
func Deploy(s *qenv.Sim, cfg Config) *Deployment {
	names := cfg.Names()
	d := &Deployment{Sim: s, Participants: map[string]*tpc.Participant{}, decide: map[string]*qenv.Service[bool, string]{}}
	crash := !cfg.NoCrashes
	d.Coordinator = &tpc.Coordinator{DB: qenv.NewStore(s, "c", qenv.Serializable), Participants: names, Mutation: cfg.Mutation}
	coord := s.Process("c", crash, d.runCoordinator)
	d.votes = qenv.NewService(s, "votes", coord, func(ctx context.Context, v vote) (tpc.Stage, error) {
		st, err := d.Coordinator.Voted(ctx, v.Node)
		if err == nil && st != tpc.Working {
			d.broadcast(ctx, st == tpc.Committed)
		}
		return st, err
	})
	for _, n := range names {
		p := &tpc.Participant{Node: n, DB: qenv.NewStore(s, n, qenv.Serializable), Mutation: cfg.Mutation}
		d.Participants[n] = p
		proc := s.Process(n, crash, func(ctx context.Context) { d.runParticipant(ctx, p) })
		d.decide[n] = qenv.NewService(s, "decide."+n, proc, func(ctx context.Context, commit bool) (string, error) {
			return "ok", p.Decide(ctx, commit)
		})
	}
	return d
}

// runCoordinator is the coordinator's main task, on boot and after every
// restart: wait for the votes, else abort, and resend a decision taken.
func (d *Deployment) runCoordinator(ctx context.Context) {
	c := d.Coordinator
	c.Restarted()
	st := c.Decision(ctx)
	if st == tpc.Working {
		qenv.Sleep(ctx, VoteTimeout)
		var err error
		if st, err = c.TimeOut(ctx); err != nil {
			return
		}
	}
	d.broadcast(ctx, st == tpc.Committed)
}

// broadcast resends the decision to each participant until it answers.
func (d *Deployment) broadcast(ctx context.Context, commit bool) {
	for _, n := range d.Coordinator.Participants {
		svc := d.decide[n]
		qenv.Go(ctx, "c.send."+n, func(ctx context.Context) {
			for {
				if _, err := svc.Call(ctx, commit, CallTimeout); err == nil {
					return
				}
				qenv.Sleep(ctx, 500*time.Millisecond)
			}
		})
	}
}

// runParticipant is a participant's main task: vote if it has not, then
// resend a vote to commit until it hears the decision.
func (d *Deployment) runParticipant(ctx context.Context, p *tpc.Participant) {
	if p.Stage(ctx) == tpc.Working {
		// option 0, nominal, is a vote to commit; one in four seeded choices
		// is a refusal
		if d.Sim.Choose("sched.vote."+p.Node, 4) == 3 {
			_, _ = p.Abort(ctx)
			return
		}
		if _, err := p.Prepare(ctx); err != nil {
			return
		}
	}
	if p.Stage(ctx) != tpc.Prepared {
		return
	}
	since := qenv.Now(ctx)
	for p.Stage(ctx) == tpc.Prepared {
		st, err := d.votes.Call(ctx, vote{p.Node}, CallTimeout)
		if err == nil && st != tpc.Working {
			_ = p.Decide(ctx, st == tpc.Committed)
			return
		}
		if qenv.Now(ctx).Sub(since) > Patience {
			if ok, _ := p.AbortPrepared(ctx); ok {
				return
			}
		}
		qenv.Sleep(ctx, 500*time.Millisecond)
	}
}

// Run deploys the protocol with c making the choices, runs it for 20 s of
// virtual time with faults, then settles for up to 60 s without. It
// closes the simulation.
func Run(c qenv.Chooser, cfg Config) (*Deployment, error) {
	s := qenv.New(c)
	defer s.Close()
	d := Deploy(s, cfg)
	if err := s.Run(20 * time.Second); err != nil {
		return d, err
	}
	return d, s.Settle(60 * time.Second)
}
