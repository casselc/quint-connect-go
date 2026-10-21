// Command gcounter is challenge 4 under Maelstrom:
//
//	maelstrom test -w g-counter --bin gcounter --node-count 3 --rate 100 --time-limit 20 --nemesis partition
//
// With $QUINT_STEPS_DIR set, each node writes its model steps to
// <dir>/<node>.jsonl:
//
//	quintgo validate -binding ../../gcounter/gcounter.binding.yaml /tmp/steps/*.jsonl
package main

import (
	"log"
	"os"
	"strconv"

	"github.com/casselc/quint-connect-go/examples/glomers/gcounter"
	"github.com/casselc/quint-connect-go/examples/glomers/maelstrom"
)

func main() {
	n := maelstrom.NewNode()
	maelstrom.StepLogFromEnv(n)
	gcounter.Setup(n, gcounter.Mutation(mutation()))
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
