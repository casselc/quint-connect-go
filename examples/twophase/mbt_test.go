package twophase_test

import (
	"context"
	"testing"

	"github.com/casselc/quint-connect-go/connect"
	"github.com/casselc/quint-connect-go/examples/twophase"
	"github.com/casselc/quint-connect-go/examples/twophase/tpc"
	"github.com/casselc/quint-connect-go/itf"
	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qtest"
)

// driver replays the Choreo model's named transitions on tpc's state
// machines, one transition per step, with no network in between: the
// model chooses which node moves and how.
type driver struct {
	mutation    tpc.Mutation
	coordinator *tpc.Coordinator
	parts       map[string]*tpc.Participant
	votes       []string // prepared, not yet counted by the coordinator
}

func (d *driver) Step(s *connect.Step) error {
	ctx := context.Background()
	node := func() *tpc.Participant { return d.parts[connect.MustPick[string](s, "node")] }
	return connect.Switch{
		"init": func(*connect.Step) error {
			sim := qenv.New(qenv.Nominal{})
			names := twophase.Config{}.Names()
			d.coordinator = &tpc.Coordinator{DB: qenv.NewStore(sim, "c", qenv.Serializable), Participants: names, Mutation: d.mutation}
			d.parts = map[string]*tpc.Participant{}
			for _, n := range names {
				d.parts[n] = &tpc.Participant{Node: n, DB: qenv.NewStore(sim, n, qenv.Serializable), Mutation: d.mutation}
			}
			return nil
		},
		// the model's broadcast is in the coordinator's inbox at once, but
		// its decision is a step of its own: the votes are counted there
		// (Voted decides on the last one, which the model may separate from
		// the decision by other nodes' steps)
		"prepare": func(*connect.Step) error {
			p := node()
			if _, err := p.Prepare(ctx); err != nil {
				return err
			}
			d.votes = append(d.votes, p.Node)
			return nil
		},
		"abort":              func(*connect.Step) error { _, err := node().Abort(ctx); return err },
		"commitAsInstructed": func(*connect.Step) error { return node().Decide(ctx, true) },
		"abortAsInstructed":  func(*connect.Step) error { return node().Decide(ctx, false) },
		"decideCommit": func(*connect.Step) error {
			for _, n := range d.votes {
				if _, err := d.coordinator.Voted(ctx, n); err != nil {
					return err
				}
			}
			d.votes = nil
			return nil
		},
		"decideAbort": func(*connect.Step) error { _, err := d.coordinator.TimeOut(ctx); return err },
	}.Step(s)
}

// nodeState is the model's local state of a node, as far as tpc has it.
type nodeState struct {
	Stage tpc.Stage `itf:"stage"`
}

func (d *driver) State() (any, error) {
	ctx := context.Background()
	out := map[string]nodeState{"c": {d.coordinator.Decision(ctx)}}
	for n, p := range d.parts {
		out[n] = nodeState{p.Stage(ctx)}
	}
	return out, nil
}

// The Choreo model drives the state machines: random traces of
// twophase3, each replayed on fresh nodes, every node's stage compared
// with the model's choreo::s.system after every step.
func TestModelDrivesTransitions(t *testing.T) {
	qtest.RequireQuint(t)
	connect.Run(t, connect.Config{Spec: "model/twophase.qnt", Main: "twophase3", MaxSamples: 30, MaxSteps: 12,
		StatePath: []string{"s", "system"}}, func() connect.Driver { return &driver{} })
}

// What the model's traces catch: committing on the first vote diverges at
// the first prepare (the model's coordinator is still Working). The
// impatient participant does not: no model trace asks a prepared
// participant to abort, so only a run whose decision is late shows it
// (TestMutants, TestHegelShrinksImpatience).
func TestModelDrivenMutants(t *testing.T) {
	qtest.RequireQuint(t)
	files, c, err := connect.GenerateRun(context.Background(), connect.Config{Spec: "model/twophase.qnt", Main: "twophase3",
		MaxSamples: 40, MaxSteps: 12, Seed: "0x2bc", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	caught := func(m tpc.Mutation) bool {
		for i, f := range files {
			tr, err := itf.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			if err := connect.Replay(tr, &driver{mutation: m}, connect.ReplayOptions{Trace: i, StatePath: []string{"s", "system"}}); err != nil {
				t.Logf("mutation %d caught (seed %s): %v", m, c.Seed, err)
				return true
			}
		}
		return false
	}
	if !caught(tpc.EarlyCommit) {
		t.Error("no model trace exposed committing on the first vote")
	}
	if caught(tpc.ImpatientParticipant) {
		t.Error("a model trace asked a prepared participant to abort, which the model forbids")
	}
}
