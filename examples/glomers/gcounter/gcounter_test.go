package gcounter

import (
	"github.com/casselc/quint-connect-go/examples/glomers/maelstrom/hegelnet"
	"math/rand/v2"
	"testing"

	"github.com/casselc/quint-connect-go/examples/glomers/maelstrom"
	"github.com/casselc/quint-connect-go/qobs"
	"github.com/casselc/quint-connect-go/qtest"
	"github.com/casselc/quint-connect-go/quinthegel"
	"hegel.dev/go/hegel"
)

var nodes = []string{"n1", "n2", "n3"}

func cluster(m Mutation) *maelstrom.Sim {
	return maelstrom.NewSim(nodes, func(n *maelstrom.Node) { Setup(n, m) })
}

func finish(s *maelstrom.Sim) {
	s.Settle(3)
	s.Step("settle")
	for _, n := range nodes {
		s.Request("c9", n, map[string]any{"type": "read"}, nil)
	}
	s.Run(nil, 1000)
}

// 15 adds at random nodes; gossip delivered in random order, some lost,
// some late (an old vector arriving after a newer one), and a partition.
func workload(s *maelstrom.Sim, seed uint64) {
	r := rand.New(rand.NewPCG(seed, 4))
	for i := 0; i < 15; i++ {
		s.Request("c1", nodes[r.IntN(3)], map[string]any{"type": "add", "delta": 1 + r.IntN(5)}, nil)
		if i == 5 {
			s.Partition([]string{"n1"}, []string{"n2", "n3"})
		}
		if i == 10 {
			s.Heal()
		}
		s.Tick(nodes[r.IntN(3)])
		for k := r.IntN(4); k > 0 && len(s.Pending()) > 0; k-- {
			// prefer the newest message: older gossip is overtaken
			p := len(s.Pending())
			i := p - 1 - r.IntN(min(p, 2))
			if r.IntN(6) == 0 {
				s.Drop(i)
			} else {
				s.Deliver(i)
			}
		}
	}
	// A last add whose gossip is lost entirely: only resending makes up for it.
	s.Run(nil, 1000)
	s.Request("c1", "n1", map[string]any{"type": "add", "delta": 7}, nil)
	s.Run(nil, 1000)
	s.Tick("n1")
	for len(s.Pending()) > 0 {
		s.Drop(0)
	}
	finish(s)
}

func record(t *testing.T, m Mutation) *qtest.Run {
	return qtest.Record(t, "gcounter.binding.yaml", func(*qobs.Recorder) { workload(cluster(m), 1) })
}

// Every add is counted once everywhere once the network is quiet, through
// loss, reordering and a partition.
func TestCounterCountsEveryAdd(t *testing.T) {
	qtest.RequireQuint(t)
	record(t, Faithful).Expect(t, qtest.Conforms())
}

// Merging by overwriting: a late, older vector takes counts back, and the
// next read returns less than the node (in the model) knows.
func TestOverwriteRejected(t *testing.T) {
	qtest.RequireQuint(t)
	record(t, Overwrite).Expect(t, qtest.Rejects("read"))
}

// Gossiping only after a change: every step is allowed, but a lost
// vector is never resent, and a node is behind once quiet.
func TestGossipOnChangeDoesNotConverge(t *testing.T) {
	qtest.RequireQuint(t)
	record(t, GossipOnChange).Expect(t, qtest.Violates("countsEveryAdd"))
}

type machine struct {
	hegelnet.Network
}

func (m *machine) RuleAdd(tc hegel.TestCase) {
	m.Sim.Request("c1", hegel.Draw(tc, hegel.SampledFrom(nodes)), map[string]any{"type": "add", "delta": hegel.Draw(tc, hegel.Integers(0, 5))}, nil)
}

func (m *machine) RuleRead(tc hegel.TestCase) {
	m.Sim.Request("c2", hegel.Draw(tc, hegel.SampledFrom(nodes)), map[string]any{"type": "read"}, nil)
}

// Hegel schedules adds, reads, deliveries, losses, partitions and timers;
// rounds are checked in bulk.
func TestHegelSchedules(t *testing.T) {
	qtest.RequireQuint(t)
	quinthegel.Test(t, quinthegel.Config{Binding: "gcounter.binding.yaml", Mode: quinthegel.Batched, Opts: []qtest.Option{qtest.Global()}, Rounds: 2},
		func(ht *hegel.T, _ *quinthegel.Case) {
			s := cluster(Faithful)
			hegel.RunStateful(ht, &machine{Network: hegelnet.Network{Sim: s, Faults: true}}, hegel.WithStatefulStepCount(25))
			finish(s)
		}, hegel.WithTestCases(25), hegel.WithDatabase(""))
}
