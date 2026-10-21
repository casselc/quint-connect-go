// Command uniqueids is challenge 2 under Maelstrom. With $QUINT_STEPS_DIR
// set, each node writes its model steps to <dir>/<node>.jsonl:
//
//	QUINT_STEPS_DIR=/tmp/steps maelstrom test -w unique-ids --bin uniqueids --time-limit 30 --rate 1000 \
//	    --node-count 3 --availability total --nemesis partition
//	quintgo validate -binding ../../uniqueids/uniqueids.binding.yaml /tmp/steps/*.jsonl
package main

import (
	"log"
	"os"
	"strconv"

	"github.com/casselc/quint-connect-go/examples/glomers/maelstrom"
	"github.com/casselc/quint-connect-go/examples/glomers/uniqueids"
)

func main() {
	n := maelstrom.NewNode()
	maelstrom.StepLogFromEnv(n)
	uniqueids.Setup(n, uniqueids.Options{Mutation: uniqueids.Mutation(mutation())})
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
