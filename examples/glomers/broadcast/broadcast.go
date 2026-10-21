// Package broadcast solves Gossip Glomers challenge 3 (3a-3e): a broadcast
// system that tolerates partitions and gossips efficiently.
//
// A node keeps the values it knows and, per neighbour, the values that
// neighbour has not acknowledged yet. On every timer tick it sends each
// neighbour all its unacknowledged values in one gossip message, and an
// acknowledgement removes them from the neighbour's backlog: batching keeps
// the message count down (3d, 3e), and resending until acknowledged gets
// values across lost messages and partitions (3c). New values learnt from
// a neighbour go into the backlogs of the other neighbours.
//
// Every model step of ../model/broadcast.qnt is recorded with Node.Step:
// broadcast (a client's value stored), gossip (a message sent), deliver (a
// message received), read (what a read returned).
package broadcast

import (
	"fmt"
	"slices"
	"time"

	"github.com/casselc/quint-connect-go/examples/glomers/maelstrom"
)

// Mutation injects a bug.
type Mutation int

const (
	Faithful Mutation = iota
	// NoRetry sends each backlog once and forgets it (fire and forget): a
	// lost message is never resent, so a partition leaves nodes behind.
	NoRetry
	// ReadOwnOnly answers reads with the values clients sent to this node
	// only, not those learnt by gossip.
	ReadOwnOnly
)

// Options configure a node.
type Options struct {
	Mutation Mutation
	// Topology overrides the topology message: a node's neighbours.
	Topology func(node string, all []string) []string
	// Interval of the gossip timer under Maelstrom (default 100ms).
	Interval time.Duration
}

type node struct {
	n       *maelstrom.Node
	o       Options
	values  map[int]bool
	own     map[int]bool            // from clients (ReadOwnOnly)
	backlog map[string]map[int]bool // neighbour -> values it has not acked
	nbrs    []string
}

func sorted(m map[int]bool) []int {
	out := make([]int, 0, len(m))
	for v := range m {
		out = append(out, v)
	}
	slices.Sort(out)
	return out
}

// Setup registers the handlers and the gossip timer.
func Setup(mn *maelstrom.Node, o Options) {
	if o.Interval == 0 {
		o.Interval = 100 * time.Millisecond
	}
	s := &node{n: mn, o: o, values: map[int]bool{}, own: map[int]bool{}, backlog: map[string]map[int]bool{}}
	mn.OnInit(func() {
		if o.Topology != nil {
			s.setNeighbours(o.Topology(mn.ID(), mn.NodeIDs()))
		}
	})
	mn.Handle("topology", func(m maelstrom.Message) error {
		var b struct {
			Topology map[string][]string `json:"topology"`
		}
		if err := m.Decode(&b); err != nil {
			return err
		}
		if o.Topology == nil {
			s.setNeighbours(b.Topology[mn.ID()])
		}
		return mn.Reply(m, map[string]any{"type": "topology_ok"})
	})
	mn.Handle("broadcast", func(m maelstrom.Message) error {
		var b struct {
			Message int `json:"message"`
		}
		if err := m.Decode(&b); err != nil {
			return err
		}
		s.own[b.Message] = true
		s.learn([]int{b.Message}, "")
		mn.Step("broadcast", nil, "node", mn.ID(), "value", b.Message)
		return mn.Reply(m, map[string]any{"type": "broadcast_ok"})
	})
	mn.Handle("read", func(m maelstrom.Message) error {
		vs := sorted(s.values)
		if o.Mutation == ReadOwnOnly {
			vs = sorted(s.own)
		}
		mn.Step("read", nil, "node", mn.ID(), "obs.values", vs)
		return mn.Reply(m, map[string]any{"type": "read_ok", "messages": vs})
	})
	mn.Handle("gossip", func(m maelstrom.Message) error {
		var b struct {
			Values []int `json:"values"`
		}
		if err := m.Decode(&b); err != nil {
			return err
		}
		mn.Step("deliver", nil, "id", fmt.Sprintf("%s/%d", m.Src, m.Header().MsgID))
		s.learn(b.Values, m.Src)
		return mn.Reply(m, map[string]any{"type": "gossip_ok"})
	})
	mn.Every(o.Interval, s.gossip)
}

func (s *node) setNeighbours(nbrs []string) {
	s.nbrs = nbrs
	for _, nb := range nbrs {
		if s.backlog[nb] == nil {
			s.backlog[nb] = map[int]bool{}
		}
	}
	// values already known go to new neighbours too
	for _, nb := range nbrs {
		for v := range s.values {
			s.backlog[nb][v] = true
		}
	}
}

// learn adds values; new ones go to every neighbour's backlog but from's.
func (s *node) learn(vs []int, from string) {
	for _, v := range vs {
		if s.values[v] {
			continue
		}
		s.values[v] = true
		for _, nb := range s.nbrs {
			if nb != from {
				s.backlog[nb][v] = true
			}
		}
	}
}

// gossip sends each neighbour its backlog; an ack clears what it carried.
func (s *node) gossip() {
	for _, nb := range s.nbrs {
		if len(s.backlog[nb]) == 0 {
			continue
		}
		vs := sorted(s.backlog[nb])
		id := fmt.Sprintf("%s/%d", s.n.ID(), s.n.NextMsgID())
		s.n.Step("gossip", nil, "id", id, "src", s.n.ID(), "dst", nb, "values", vs)
		nb := nb
		_, _ = s.n.RPC(nb, map[string]any{"type": "gossip", "values": vs}, func(reply maelstrom.Message) {
			for _, v := range vs {
				delete(s.backlog[nb], v)
			}
		})
		if s.o.Mutation == NoRetry {
			s.backlog[nb] = map[int]bool{}
		}
	}
}

// Tree is a topology for n nodes: a spanning tree with fan-out k, which
// keeps messages per broadcast near n-1 (challenge 3e's message budget).
func Tree(k int) func(string, []string) []string {
	return func(node string, all []string) []string {
		i := slices.Index(all, node)
		var out []string
		if i > 0 {
			out = append(out, all[(i-1)/k])
		}
		for c := k*i + 1; c <= k*i+k && c < len(all); c++ {
			out = append(out, all[c])
		}
		return out
	}
}
