package uniqueids

import (
	"math/rand/v2"
	"os/exec"
	"testing"

	"github.com/casselc/quint-connect-go/examples/glomers/maelstrom"
	"github.com/casselc/quint-connect-go/qobs"
	"github.com/casselc/quint-connect-go/qtest"
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

var nodes = []string{"n1", "n2", "n3"}

func sim(m Mutation, seed uint64) *maelstrom.Sim {
	r := rand.New(rand.NewPCG(seed, 1))
	return maelstrom.NewSim(nodes, func(n *maelstrom.Node) { Setup(n, Options{Mutation: m, Incarnation: r.Uint64}) })
}

// workload: concurrent generate requests in random delivery order, a
// partition, and a restart of n1 half-way.
func workload(s *maelstrom.Sim, seed uint64) {
	r := rand.New(rand.NewPCG(seed, 2))
	pick := func(n int) int { return r.IntN(n) }
	for i := 0; i < 30; i++ {
		s.Request("c1", nodes[r.IntN(len(nodes))], map[string]any{"type": "generate"}, nil)
		if i == 10 {
			s.Partition([]string{"n1"}, []string{"n2", "n3"}) // totally available: ids still flow
		}
		if i == 15 {
			s.Restart("n1")
		}
		if r.IntN(3) == 0 {
			s.Run(pick, 3)
		}
	}
	s.Run(pick, 1000)
}

func check(t *testing.T, m Mutation) *qtest.Run {
	return qtest.Record(t, "uniqueids.binding.yaml", func(*qobs.Recorder) { workload(sim(m, 1), 1) })
}

func TestUniqueUnderPartitionAndRestart(t *testing.T) {
	needQuint(t)
	if run := check(t, Faithful); !run.OK() {
		t.Fatalf("faithful ids are not unique:\n%s", run.Report.Summary())
	}
}

// Both mutants are behaviours the model allows (a node may answer any id)
// that violate its property: the verdict is "conforms, idsUnique FAIL",
// at the step that repeated an id.
func TestMutantsRepeatIDs(t *testing.T) {
	needQuint(t)
	for _, m := range []Mutation{CounterOnly, NoIncarnation} {
		run := check(t, m)
		a := run.Report.Actors[0]
		if a.Conforms != validate.Pass || a.Invariants[0].Verdict != validate.Fail {
			t.Errorf("mutant %d:\n%s", m, run.Report.Summary())
			continue
		}
		t.Logf("mutant %d: duplicate at %s", m, a.Invariants[0].Failure.Call)
	}
}

// Hegel schedules the network (deliveries, losses, partitions) and the
// requests and restarts; runs are checked in bulk, round after round.
type machine struct {
	maelstrom.Network
}

func (m *machine) RuleGenerate(tc hegel.TestCase) {
	m.Sim.Request("c1", hegel.Draw(tc, hegel.SampledFrom(nodes)), map[string]any{"type": "generate"}, nil)
}

func (m *machine) RuleRestart(tc hegel.TestCase) {
	m.Sim.Restart(hegel.Draw(tc, hegel.SampledFrom(nodes)))
}

func TestHegelSchedules(t *testing.T) {
	needQuint(t)
	quinthegel.Test(t, quinthegel.Config{Binding: "uniqueids.binding.yaml", Mode: quinthegel.Batched, Global: true, AllowEmpty: true, Rounds: 2},
		func(ht *hegel.T, _ *quinthegel.Recorder) {
			s := sim(Faithful, uint64(hegel.Draw(ht, hegel.Integers(0, 1<<30))))
			hegel.RunStateful(ht, &machine{Network: maelstrom.Network{Sim: s, Faults: true, NoTimers: true}}, hegel.WithStatefulStepCount(25))
		}, hegel.WithTestCases(30), hegel.WithDatabase(""))
}
