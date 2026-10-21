package maelstrom

import (
	"encoding/json"
	"testing"
)

// Echo (challenge 1) runs on the Sim; replies carry in_reply_to and the
// Lamport clock moves forward across messages.
func TestEchoAndLamport(t *testing.T) {
	s := NewSim([]string{"n1", "n2"}, func(n *Node) {
		n.Handle("echo", func(m Message) error {
			var b map[string]any
			_ = m.Decode(&b)
			b["type"] = "echo_ok"
			return n.Reply(m, b)
		})
	})
	var got map[string]any
	s.Request("c1", "n1", map[string]any{"type": "echo", "echo": "hi"}, func(r Message) { _ = json.Unmarshal(r.Body, &got) })
	s.Run(nil, 10)
	if got["type"] != "echo_ok" || got["echo"] != "hi" || got["in_reply_to"] == nil || got["lamport"] != nil {
		t.Fatalf("reply %v", got)
	}
	// n1 -> n2 RPC: n2's clock passes n1's.
	n1, n2 := s.Nodes["n1"], s.Nodes["n2"]
	n2.Handle("ping", func(m Message) error { return n2.Reply(m, map[string]any{"type": "pong"}) })
	replied := false
	n1.lamport = 50
	if _, err := n1.RPC("n2", map[string]any{"type": "ping"}, func(Message) { replied = true }); err != nil {
		t.Fatal(err)
	}
	s.Run(nil, 10)
	if !replied || n2.lamport <= 50 || n1.lamport <= n2.lamport-1 {
		t.Fatalf("replied %v, clocks n1 %d n2 %d", replied, n1.lamport, n2.lamport)
	}
}

func TestPartitionLosesMessages(t *testing.T) {
	s := NewSim([]string{"n1", "n2", "n3"}, func(n *Node) { n.Handle("x", func(Message) error { return nil }) })
	s.Partition([]string{"n1"}, []string{"n2", "n3"})
	_ = s.Nodes["n1"].Send("n2", map[string]any{"type": "x"})
	_ = s.Nodes["n2"].Send("n3", map[string]any{"type": "x"})
	s.Run(nil, 10)
	if s.Lost != 1 || s.Delivered != 1 {
		t.Fatalf("lost %d delivered %d", s.Lost, s.Delivered)
	}
}
