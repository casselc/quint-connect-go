package kafka

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

var (
	nodes = []string{"n1", "n2"}
	keys  = []string{"k1", "k2"}
)

func cluster(m Mutation) *maelstrom.Sim {
	s := maelstrom.NewSim(nodes, func(n *maelstrom.Node) { Setup(n, m) })
	s.AddService(Service, KV())
	return s
}

// workload: producers send to both nodes concurrently, consumers poll,
// commit and list offsets, in random delivery order.
func workload(s *maelstrom.Sim, seed uint64) {
	r := rand.New(rand.NewPCG(seed, 5))
	run := func() { s.Run(func(n int) int { return r.IntN(n) }, r.IntN(5)) }
	for i := 0; i < 24; i++ {
		node, key := nodes[r.IntN(2)], keys[r.IntN(2)]
		switch r.IntN(4) {
		case 0, 1:
			s.Request("c1", node, map[string]any{"type": "send", "key": key, "msg": 100 + i}, nil)
		case 2:
			s.Request("c2", node, map[string]any{"type": "poll", "offsets": map[string]int{key: r.IntN(3)}}, nil)
		case 3:
			if r.IntN(2) == 0 {
				s.Request("c2", node, map[string]any{"type": "commit_offsets", "offsets": map[string]int{key: r.IntN(2)}}, nil)
			} else {
				s.Request("c2", node, map[string]any{"type": "list_committed_offsets", "keys": keys}, nil)
			}
		}
		run()
	}
	s.Run(func(n int) int { return r.IntN(n) }, 100000)
	s.Step("settle")
}

func record(t *testing.T, m Mutation) *qtest.Run {
	return qtest.Record(t, "kafka.binding.yaml", func(*qobs.Recorder) { workload(cluster(m), 3) })
}

func TestLogKeepsEverySend(t *testing.T) {
	qtest.RequireQuint(t)
	record(t, Faithful).Expect(t, qtest.Conforms())
}

// Blind appends conform step by step (lin-kv allows a write), but an
// acknowledged message is overwritten: sentAtItsOffset fails.
func TestBlindAppendLosesSends(t *testing.T) {
	qtest.RequireQuint(t)
	record(t, BlindAppend).Expect(t, qtest.Violates("sentAtItsOffset"))
}

// A poll that skips a message returns something the model's log does not
// have at those offsets.
func TestPollSkipsRejected(t *testing.T) {
	qtest.RequireQuint(t)
	record(t, PollSkips).Expect(t, qtest.Mismatch("pollOk"))
}

type machine struct {
	hegelnet.Network
	msg int
}

func (m *machine) RuleSend(tc hegel.TestCase) {
	m.msg++
	m.Sim.Request("c1", hegel.Draw(tc, hegel.SampledFrom(nodes)),
		map[string]any{"type": "send", "key": hegel.Draw(tc, hegel.SampledFrom(keys)), "msg": m.msg}, nil)
}

func (m *machine) RulePoll(tc hegel.TestCase) {
	m.Sim.Request("c2", hegel.Draw(tc, hegel.SampledFrom(nodes)),
		map[string]any{"type": "poll", "offsets": map[string]int{hegel.Draw(tc, hegel.SampledFrom(keys)): hegel.Draw(tc, hegel.Integers(0, 3))}}, nil)
}

func (m *machine) RuleCommit(tc hegel.TestCase) {
	m.Sim.Request("c2", hegel.Draw(tc, hegel.SampledFrom(nodes)),
		map[string]any{"type": "commit_offsets", "offsets": map[string]int{hegel.Draw(tc, hegel.SampledFrom(keys)): hegel.Draw(tc, hegel.Integers(0, 2))}}, nil)
}

func (m *machine) RuleList(tc hegel.TestCase) {
	m.Sim.Request("c2", hegel.Draw(tc, hegel.SampledFrom(nodes)), map[string]any{"type": "list_committed_offsets", "keys": keys}, nil)
}

func TestHegelSchedules(t *testing.T) {
	qtest.RequireQuint(t)
	quinthegel.Test(t, quinthegel.Config{Binding: "kafka.binding.yaml", Mode: quinthegel.Batched, Opts: []qtest.Option{qtest.Global()}, AllowEmpty: true, Rounds: 2},
		func(ht *hegel.T, _ *quinthegel.Case) {
			s := cluster(Faithful)
			hegel.RunStateful(ht, &machine{Network: hegelnet.Network{Sim: s, NoTimers: true}}, hegel.WithStatefulStepCount(20))
			s.Run(nil, 100000)
			s.Step("settle")
		}, hegel.WithTestCases(25), hegel.WithDatabase(""))
}
