// Package gcounter solves Gossip Glomers challenge 4 with a G-counter CRDT:
// each node counts the adds it acknowledged in its own entry of a vector,
// gossips the vector to every other node on each timer tick, and merges
// what it receives by taking the maximum of each entry. A read is the sum.
// Gossip is resent on every tick, so lost messages and partitions only
// delay convergence. Nothing depends on seq-kv's consistency (see the
// README for the two seq-kv designs that Maelstrom rejected).
//
// The nodes record add, gossip, deliver and read (../model/gcounter.qnt).
package gcounter

import (
	"fmt"
	"maps"
	"time"

	"github.com/casselc/quint-connect-go/examples/glomers/maelstrom"
)

// Mutation injects a bug.
type Mutation int

const (
	Faithful Mutation = iota
	// Overwrite replaces entries with the received ones instead of taking
	// the maximum: an old gossip arriving late takes counts back.
	Overwrite
	// GossipOnChange only gossips after a local add: a lost message is
	// never made up for, and nodes stay behind after a partition.
	GossipOnChange
)

// Setup registers the handlers and the gossip timer.
func Setup(n *maelstrom.Node, m Mutation) {
	vec := map[string]int{}
	changed := false
	total := func() int {
		t := 0
		for _, c := range vec {
			t += c
		}
		return t
	}
	n.Handle("add", func(req maelstrom.Message) error {
		var b struct {
			Delta int `json:"delta"`
		}
		if err := req.Decode(&b); err != nil {
			return err
		}
		vec[n.ID()] += b.Delta
		changed = true
		n.Step("add", nil, "node", n.ID(), "delta", b.Delta)
		return n.Reply(req, map[string]any{"type": "add_ok"})
	})
	n.Handle("read", func(req maelstrom.Message) error {
		v := total()
		n.Step("read", nil, "node", n.ID(), "value", v)
		return n.Reply(req, map[string]any{"type": "read_ok", "value": v})
	})
	n.Handle("gossip", func(msg maelstrom.Message) error {
		var b struct {
			Vec map[string]int `json:"vec"`
		}
		if err := msg.Decode(&b); err != nil {
			return err
		}
		n.Step("deliver", nil, "id", fmt.Sprintf("%s/%d", msg.Src, msg.Header().MsgID))
		for k, c := range b.Vec {
			if m == Overwrite || c > vec[k] {
				vec[k] = c
			}
		}
		return nil
	})
	n.Every(200*time.Millisecond, func() {
		if m == GossipOnChange && !changed {
			return
		}
		changed = false
		for _, dst := range n.NodeIDs() {
			if dst == n.ID() {
				continue
			}
			v := maps.Clone(vec)
			id := fmt.Sprintf("%s/%d", n.ID(), n.NextMsgID())
			n.Step("gossip", nil, "id", id, "src", n.ID(), "dst", dst, "vec", v)
			_, _ = n.RPC(dst, map[string]any{"type": "gossip", "vec": v}, nil)
		}
	})
}
