// Command kafka is challenge 5b/5c under Maelstrom:
//
//	maelstrom test -w kafka --bin kafka --node-count 2 --concurrency 2n --time-limit 20 --rate 1000
//
// Its model steps include lin-kv's own operations, which only the in-process
// Sim records: check traces in tests.
package main

import (
	"log"
	"os"
	"strconv"

	"github.com/casselc/quint-connect-go/examples/glomers/kafka"
	"github.com/casselc/quint-connect-go/examples/glomers/maelstrom"
)

func main() {
	n := maelstrom.NewNode()
	kafka.Setup(n, kafka.Mutation(mutation()))
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
