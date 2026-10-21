// Package uniqueids solves Gossip Glomers challenge 2: globally unique ids,
// with no coordination. An id is the node id, the node's incarnation (a
// random number drawn at start-up, so a restarted node does not repeat
// itself) and a counter.
package uniqueids

import (
	"fmt"
	"math/rand/v2"

	"github.com/casselc/quint-connect-go/examples/glomers/maelstrom"
)

// Mutation injects a bug.
type Mutation int

const (
	Faithful Mutation = iota
	// CounterOnly uses only the counter: every node issues 1, 2, 3, ...
	CounterOnly
	// NoIncarnation leaves out the incarnation: a restarted node starts
	// its counter again and repeats its own ids.
	NoIncarnation
)

// Options configure a node.
type Options struct {
	Mutation Mutation
	// Incarnation draws the incarnation number (default: random).
	Incarnation func() uint64
}

// Setup registers the generate handler.
func Setup(n *maelstrom.Node, o Options) {
	if o.Incarnation == nil {
		o.Incarnation = rand.Uint64
	}
	inc := o.Incarnation()
	counter := 0
	n.Handle("generate", func(m maelstrom.Message) error {
		counter++
		var id string
		switch o.Mutation {
		case CounterOnly:
			id = fmt.Sprint(counter)
		case NoIncarnation:
			id = fmt.Sprintf("%s-%d", n.ID(), counter)
		default:
			id = fmt.Sprintf("%s-%x-%d", n.ID(), inc, counter)
		}
		n.Step("generate", nil, "node", n.ID(), "id", id)
		return n.Reply(m, map[string]any{"type": "generate_ok", "id": id})
	})
}
