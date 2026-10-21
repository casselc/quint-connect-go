// Command echo is Gossip Glomers challenge 1 under Maelstrom:
//
//	maelstrom test -w echo --bin echo --node-count 1 --time-limit 10
package main

import (
	"log"

	"github.com/casselc/quint-connect-go/examples/glomers/maelstrom"
)

func main() {
	n := maelstrom.NewNode()
	n.Handle("echo", func(m maelstrom.Message) error {
		var b map[string]any
		if err := m.Decode(&b); err != nil {
			return err
		}
		b["type"] = "echo_ok"
		delete(b, "msg_id")
		delete(b, "lamport")
		return n.Reply(m, b)
	})
	if err := n.Run(); err != nil {
		log.Fatal(err)
	}
}
