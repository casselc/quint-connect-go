// Package uniqueids solves Gossip Glomers challenge 2: globally unique ids,
// with no coordination. An id is the node id, the node's incarnation (a
// random number drawn at start-up, so a restarted node does not repeat
// itself) and a counter (see ID).
package uniqueids

import (
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"

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
	// FixedIncarnation encodes the incarnation, but every start draws the
	// same one (0), so a restarted node repeats its own ids too.
	FixedIncarnation
)

// ID encodes (node, incarnation, counter) as an id: node-<incarnation in
// hex>-<counter>, read from the right, so it is injective whatever the
// node id contains (ParseID inverts it). The mutants' encodings drop parts
// of it.
func ID(m Mutation, node string, incarnation uint64, counter int) string {
	switch m {
	case CounterOnly:
		return strconv.Itoa(counter)
	case NoIncarnation:
		return fmt.Sprintf("%s-%d", node, counter)
	}
	return fmt.Sprintf("%s-%x-%d", node, incarnation, counter)
}

// ParseID inverts ID for the faithful encoding.
func ParseID(id string) (node string, incarnation uint64, counter int, err error) {
	i := strings.LastIndexByte(id, '-')
	if i < 0 {
		return "", 0, 0, fmt.Errorf("id %q: no counter", id)
	}
	j := strings.LastIndexByte(id[:i], '-')
	if j < 0 {
		return "", 0, 0, fmt.Errorf("id %q: no incarnation", id)
	}
	if counter, err = strconv.Atoi(id[i+1:]); err != nil {
		return "", 0, 0, fmt.Errorf("id %q: counter: %w", id, err)
	}
	if incarnation, err = strconv.ParseUint(id[j+1:i], 16, 64); err != nil {
		return "", 0, 0, fmt.Errorf("id %q: incarnation: %w", id, err)
	}
	return id[:j], incarnation, counter, nil
}

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
	if o.Mutation == FixedIncarnation {
		inc = 0
	}
	counter := 0
	n.Handle("generate", func(m maelstrom.Message) error {
		counter++
		id := ID(o.Mutation, n.ID(), inc, counter)
		// The id, and what it is built from: uniqueids.binding.yaml checks
		// the ids, uniqueids.scheme.binding.yaml the parts.
		n.Step("generate", nil, "node", n.ID(), "id", id, "incarnation", fmt.Sprintf("%x", inc), "counter", counter)
		return n.Reply(m, map[string]any{"type": "generate_ok", "id": id})
	})
}
