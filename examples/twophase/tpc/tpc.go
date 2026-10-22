// Package tpc is two-phase commit's state machines: participants that
// vote and apply the decision, and a coordinator that decides. Each node
// keeps its stage in its own durable store (qenv.DB), and records a model
// step (model/twophase.qnt) when a transition commits there, where it takes
// effect: a crash after the commit does not lose the step, one before it
// loses both. Messages, retries and timeouts are the caller's (package
// twophase), so the model can drive these transitions one by one.
package tpc

import (
	"context"
	"errors"

	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qobs"
)

// Stage is a node's progress, as in the model.
type Stage string

// The stages.
const (
	Working   Stage = "Working"
	Prepared  Stage = "Prepared"
	Committed Stage = "Committed"
	Aborted   Stage = "Aborted"
)

// Mutation selects a bug.
type Mutation int

// The bugs.
const (
	Faithful Mutation = iota
	// EarlyCommit: the coordinator commits on the first vote, not all.
	EarlyCommit
	// ImpatientParticipant: a prepared participant that hears no decision
	// in time aborts on its own (the runtime asks it to, AbortPrepared). It
	// voted to commit, so the coordinator may commit without it.
	ImpatientParticipant
	// VolatileDecision: the coordinator keeps its decision in memory. A
	// coordinator that crashes after deciding to commit decides again.
	VolatileDecision
)

const stageKey = "stage"

// ErrNotPrepared is a decision a participant cannot apply in its stage.
var ErrNotPrepared = errors.New("tpc: commit decision for a participant that has not prepared")

func stageIn(tx qenv.Txn) Stage {
	if v, ok := tx.Get(stageKey); ok {
		return v.(Stage)
	}
	return Working
}

// move changes the stage from one of from to to, recording step (with kv)
// when the change commits; it reports whether it moved.
func move(ctx context.Context, db qenv.DB, from []Stage, to Stage, step string, kv ...any) (bool, error) {
	moved := false
	err := qenv.Retry(ctx, db, 5, func(tx qenv.Txn) error {
		moved = false
		cur := stageIn(tx)
		for _, f := range from {
			if cur == f {
				tx.Put(stageKey, to)
				tx.OnCommit(func() { qobs.Record(ctx, step, nil, kv...) })
				moved = true
			}
		}
		return nil
	})
	return moved, err
}

// A Participant votes and applies the coordinator's decision.
type Participant struct {
	Node     string
	DB       qenv.DB
	Mutation Mutation
}

// Stage is the participant's durable stage.
func (p *Participant) Stage(ctx context.Context) Stage { return read(ctx, p.DB) }

func read(ctx context.Context, db qenv.DB) Stage {
	var s Stage
	_ = qenv.Retry(ctx, db, 5, func(tx qenv.Txn) error { s = stageIn(tx); return nil })
	return s
}

// Prepare votes to commit: Working to Prepared (prepare).
func (p *Participant) Prepare(ctx context.Context) (bool, error) {
	return move(ctx, p.DB, []Stage{Working}, Prepared, "prepare", "node", p.Node)
}

// Abort refuses: Working to Aborted (abort).
func (p *Participant) Abort(ctx context.Context) (bool, error) {
	return move(ctx, p.DB, []Stage{Working}, Aborted, "abort", "node", p.Node)
}

// AbortPrepared is the impatient participant's way out: only the
// ImpatientParticipant mutant takes it, and records it as an abort.
func (p *Participant) AbortPrepared(ctx context.Context) (bool, error) {
	if p.Mutation != ImpatientParticipant {
		return false, nil
	}
	return move(ctx, p.DB, []Stage{Prepared}, Aborted, "abort", "node", p.Node)
}

// Decide applies the coordinator's decision (commitAsInstructed,
// abortAsInstructed). Applying it again is a no-op, so redelivery is safe.
func (p *Participant) Decide(ctx context.Context, commit bool) error {
	if commit {
		if s := p.Stage(ctx); s == Working || s == Aborted {
			return ErrNotPrepared
		}
		_, err := move(ctx, p.DB, []Stage{Prepared}, Committed, "commitAsInstructed", "node", p.Node)
		return err
	}
	_, err := move(ctx, p.DB, []Stage{Working, Prepared, Committed}, Aborted, "abortAsInstructed", "node", p.Node)
	return err
}

// A Coordinator collects votes and decides.
type Coordinator struct {
	DB           qenv.DB
	Participants []string
	Mutation     Mutation

	votes    map[string]bool // in memory: participants vote until decided
	volatile Stage           // the decision, under VolatileDecision
}

// Decision is the coordinator's stage: Working until it decides.
func (c *Coordinator) Decision(ctx context.Context) Stage {
	if c.Mutation == VolatileDecision {
		if c.volatile == "" {
			return Working
		}
		return c.volatile
	}
	return read(ctx, c.DB)
}

func (c *Coordinator) decide(ctx context.Context, to Stage, step string) (bool, error) {
	if c.Mutation == VolatileDecision {
		if c.Decision(ctx) != Working {
			return false, nil
		}
		c.volatile = to
		qobs.Record(ctx, step, nil)
		return true, nil
	}
	return move(ctx, c.DB, []Stage{Working}, to, step)
}

// Voted notes node's vote to commit, and decides to commit once every
// participant has voted (decideCommit). It returns the decision so far.
func (c *Coordinator) Voted(ctx context.Context, node string) (Stage, error) {
	if c.votes == nil {
		c.votes = map[string]bool{}
	}
	c.votes[node] = true
	all := true
	for _, p := range c.Participants {
		all = all && c.votes[p]
	}
	if c.Decision(ctx) == Working && (all || c.Mutation == EarlyCommit) {
		if _, err := c.decide(ctx, Committed, "decideCommit"); err != nil {
			return Working, err
		}
	}
	return c.Decision(ctx), nil
}

// TimeOut decides to abort if the coordinator has not decided
// (decideAbort).
func (c *Coordinator) TimeOut(ctx context.Context) (Stage, error) {
	if _, err := c.decide(ctx, Aborted, "decideAbort"); err != nil {
		return Working, err
	}
	return c.Decision(ctx), nil
}

// Restarted forgets what a crash loses: the votes, and under
// VolatileDecision the decision.
func (c *Coordinator) Restarted() { c.votes, c.volatile = nil, "" }
