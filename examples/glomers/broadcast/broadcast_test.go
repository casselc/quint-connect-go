package broadcast

import (
	"math/rand/v2"
	"os/exec"
	"testing"

	"github.com/casselc/quint-connect-go/examples/glomers/maelstrom"
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

var nodes = []string{"n0", "n1", "n2", "n3", "n4"}

func cluster(m Mutation) *maelstrom.Sim {
	return maelstrom.NewSim(nodes, func(n *maelstrom.Node) { Setup(n, Options{Mutation: m, Topology: Tree(2)}) })
}

// finish lets the system quiesce, records that, and reads every node:
// after settle, the model requires every read to return every value.
func finish(s *maelstrom.Sim) {
	s.Settle(10)
	s.Step("settle")
	for _, n := range nodes {
		s.Request("c9", n, map[string]any{"type": "read"}, nil)
	}
	s.Run(nil, 100)
}

// workload: 20 broadcasts to random nodes while messages are delivered in
// random order, some lost, and the tree is cut in two for a while.
func workload(s *maelstrom.Sim, seed uint64) {
	r := rand.New(rand.NewPCG(seed, 3))
	for v := 1; v <= 20; v++ {
		s.Request("c1", nodes[r.IntN(len(nodes))], map[string]any{"type": "broadcast", "message": v}, nil)
		switch v {
		case 5:
			s.Partition([]string{"n0", "n1", "n3"}, []string{"n2", "n4"})
		case 15:
			s.Heal()
		}
		s.Tick(nodes[r.IntN(len(nodes))])
		for k := r.IntN(6); k > 0 && len(s.Pending()) > 0; k-- {
			i := r.IntN(len(s.Pending()))
			if r.IntN(8) == 0 {
				s.Drop(i)
			} else {
				s.Deliver(i)
			}
		}
	}
	finish(s)
}

func record(t *testing.T, m Mutation) (*qtest.Run, *maelstrom.Sim) {
	var s *maelstrom.Sim
	run := qtest.Record(t, "broadcast.binding.yaml", func(*qobs.Recorder) {
		s = cluster(m)
		workload(s, 1)
	})
	return run, s
}

// Under loss, reordering and a partition, every node ends up with every
// value; reads only ever return broadcast values.
func TestBroadcastConverges(t *testing.T) {
	needQuint(t)
	run, s := record(t, Faithful)
	if !run.OK() {
		t.Fatalf("%s", run.Report.Summary())
	}
	t.Logf("%d steps; %d messages delivered, %d lost (%.1f messages per broadcast)", len(run.Steps), s.Delivered, s.Lost,
		float64(s.Delivered+s.Lost)/20)
}

// Fire-and-forget gossip: every step is one the model allows (nothing is
// invented), but after the network heals some node never learns what it
// missed. The verdict is "conforms, convergedWhenSettled FAIL", at settle.
func TestNoRetryDoesNotConverge(t *testing.T) {
	needQuint(t)
	run, _ := record(t, NoRetry)
	a := run.Report.Actors[0]
	inv := a.Invariants[1]
	if a.Conforms != validate.Pass || inv.Name != "convergedWhenSettled" || inv.Verdict != validate.Fail {
		t.Fatalf("want convergence to fail after settle:\n%s", run.Report.Summary())
	}
	t.Logf("as expected:\n%s", run.Report.Summary())
}

// A read that misses gossiped values returns a state the model's node does
// not have.
func TestReadOwnOnlyRejected(t *testing.T) {
	needQuint(t)
	run, _ := record(t, ReadOwnOnly)
	f := run.Report.Actors[0].Failure
	if run.OK() || f == nil || f.Kind != "state" || f.Step.Action != "read" {
		t.Fatalf("want a read the model rejects:\n%s", run.Report.Summary())
	}
}

// ---- Hegel schedules the network ---------------------------------------------

type machine struct {
	maelstrom.Network
	next int
}

func (m *machine) RuleBroadcast(tc hegel.TestCase) {
	m.next++
	m.Sim.Request("c1", hegel.Draw(tc, hegel.SampledFrom(nodes)), map[string]any{"type": "broadcast", "message": m.next}, nil)
}

func scenario(tc hegel.TestCase, mut Mutation, steps int) {
	s := cluster(mut)
	hegel.RunStateful(tc, &machine{Network: maelstrom.Network{Sim: s, Faults: true}}, hegel.WithStatefulStepCount(steps))
	finish(s)
}

// Hegel explores schedules: deliveries in any order, losses, partitions,
// timers. Rounds of runs are checked in bulk against the model.
func TestHegelSchedulesConverge(t *testing.T) {
	needQuint(t)
	quinthegel.Test(t, quinthegel.Config{Binding: "broadcast.binding.yaml", Mode: quinthegel.Batched, Global: true, Rounds: 2},
		func(ht *hegel.T, _ *quinthegel.Recorder) { scenario(ht, Faithful, 30) },
		hegel.WithTestCases(25), hegel.WithDatabase(""))
}

// Hegel finds, and shrinks, a schedule in which NoRetry loses a value for
// good: a broadcast, its gossip lost, and settle.
func TestHegelShrinksNoRetry(t *testing.T) {
	needQuint(t)
	if testing.Short() {
		t.Skip("shrinking against the model")
	}
	c, err := quinthegel.Checker("broadcast.binding.yaml", validate.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	cc := quinthegel.NewCaseChecker(c)
	var last []qtrace.Step
	err = hegel.Run(func(tc hegel.TestCase) {
		rec := quinthegel.NewRecorder(tc)
		defer rec.Install()()
		scenario(tc, NoRetry, 20)
		last = rec.Steps()
		quinthegel.CheckCase(tc, cc, last)
	}, hegel.WithTestCases(40), hegel.WithDatabase(""), hegel.WithSeed(2))
	q, hits := cc.Stats()
	t.Logf("%d quint checks, %d from the cache; shrunk run:\n%s", q, hits, quinthegel.Render(last))
	if err == nil {
		t.Fatal("NoRetry passed")
	}
}
