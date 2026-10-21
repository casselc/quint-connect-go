// Command broadcast is challenge 3 (3a-3e) under Maelstrom, gossiping on a
// spanning tree of fan-out 4. With $QUINT_STEPS_DIR set, each node writes
// its model steps to <dir>/<node>.jsonl:
//
//	QUINT_STEPS_DIR=/tmp/steps maelstrom test -w broadcast --bin broadcast --node-count 5 --time-limit 20 \
//	    --rate 10 --nemesis partition
//	quintgo validate -binding ../../broadcast/broadcast.binding.yaml /tmp/steps/*.jsonl
package main

import (
	"log"
	"os"
	"strconv"
	"time"

	"github.com/casselc/quint-connect-go/examples/glomers/broadcast"
	"github.com/casselc/quint-connect-go/examples/glomers/maelstrom"
)

func main() {
	n := maelstrom.NewNode()
	maelstrom.StepLogFromEnv(n)
	broadcast.Setup(n, broadcast.Options{Topology: broadcast.Tree(4), Interval: 100 * time.Millisecond, Mutation: broadcast.Mutation(mutation())})
	if err := n.Run(); err != nil {
		log.Fatal(err)
	}
}

// mutation reads $GLOMERS_MUTATION: the number of a Mutation of the
// package (0, the default, is the faithful implementation).
func mutation() int {
	m, _ := strconv.Atoi(os.Getenv("GLOMERS_MUTATION"))
	return m
}
