//go:build sim

// The broadcast cluster inside gosim: three machines, each running the
// Glomers broadcast node unchanged over real TCP (tcp.Transport), with
// gosim simulating the network, the timers and the scheduler. A client on
// the test's own machine broadcasts values to random nodes while a nemesis
// isolates one node at a time; then the network heals, the cluster
// settles, and every node is read.
//
// Every choice comes from gosimruntime.Choose, and so from the runner's
// Chooser (see ../host_test.go): which node a value goes to, how long the
// client waits, which node is cut off ("fault.partition") and for how long.
// Steps are recorded with qobs, as in the other Glomers tests, into a sink
// that sends them to the runner (qgosim.Sink).
package cluster_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jellevandenhooff/gosim"
	"github.com/jellevandenhooff/gosim/gosimruntime"

	"github.com/casselc/quint-connect-go/examples/glomers/broadcast"
	"github.com/casselc/quint-connect-go/examples/glomers/maelstrom"
	"github.com/casselc/quint-connect-go/examples/gosim/broadcast/tcp"
	"github.com/casselc/quint-connect-go/qgosim"
	"github.com/casselc/quint-connect-go/qobs"
)

var ids = []string{"n0", "n1", "n2"}

// addr is the TCP address of a node, or of the client on the test machine
// (gosim gives the first machine 11.0.0.1).
func addr(id string) string {
	switch id {
	case "c1":
		return "11.0.0.1:4000"
	case "n0", "n1", "n2":
		return fmt.Sprintf("10.0.0.%c:4000", id[1]+1)
	}
	return ""
}

func host(id string) string { h, _, _ := net.SplitHostPort(addr(id)); return h }

func mutation() broadcast.Mutation {
	switch os.Getenv("BROADCAST_MUTATION") {
	case "NoRetry":
		return broadcast.NoRetry
	case "ReadOwnOnly":
		return broadcast.ReadOwnOnly
	}
	return broadcast.Faithful
}

// cluster holds the running nodes. Machines have their own globals, so it
// is shared through the closures that start them, not a package variable.
type cluster struct {
	mu    sync.Mutex
	nodes map[string]*maelstrom.Node
}

func initMsg(id string) maelstrom.Message {
	body, _ := json.Marshal(map[string]any{"type": "init", "msg_id": 1, "node_id": id, "node_ids": ids})
	return maelstrom.Message{Src: "c0", Dest: id, Body: body}
}

// node is the main function of a node's machine.
func (c *cluster) node(id string, sink qobs.Sink) func() {
	return func() {
		n := maelstrom.NewNode()
		broadcast.Setup(n, broadcast.Options{Mutation: mutation(), Topology: broadcast.Tree(2)})
		n.SetRecorder(qobs.NewRecorder(sink, qobs.WithID(id)))
		ln, err := net.Listen("tcp", addr(id))
		if err != nil {
			panic(err)
		}
		n.Start(&tcp.Transport{Addr: addr})
		_ = n.Receive(initMsg(id))
		c.mu.Lock()
		c.nodes[id] = n
		c.mu.Unlock()
		_ = tcp.Serve(ln, n)
	}
}

// isolate cuts node i off from the other nodes (not from the client), or
// reconnects it.
func isolate(i int, cut bool) {
	for j, other := range ids {
		if j != i {
			if err := gosim.SetConnected(host(ids[i]), host(other), !cut); err != nil {
				panic(err)
			}
		}
	}
}

func TestBroadcast(t *testing.T) {
	sink := qgosim.Sink()
	c := &cluster{nodes: map[string]*maelstrom.Node{}}
	for _, id := range ids {
		gosim.NewMachine(gosim.MachineConfig{Label: id, Addr: netip.MustParseAddr(host(id)), MainFunc: c.node(id, sink)})
	}

	client := maelstrom.NewNode()
	client.Start(&tcp.Transport{Addr: addr})
	_ = client.Receive(initMsg("c1"))
	time.Sleep(100 * time.Millisecond) // the nodes listen

	cut := -1 // the isolated node, if any
	for v := 1; v <= 10; v++ {
		if cut < 0 {
			if k := gosimruntime.Choose("fault.partition", len(ids)+1); k > 0 {
				cut = k - 1
				isolate(cut, true)
			}
		} else if gosimruntime.Choose("partition.heal", 3) == 0 {
			isolate(cut, false)
			cut = -1
		}
		to := ids[gosimruntime.Choose("client.node", len(ids))]
		_, _ = client.RPC(to, map[string]any{"type": "broadcast", "message": v}, nil)
		time.Sleep(time.Duration(gosimruntime.Choose("client.wait", 4)) * 50 * time.Millisecond)
	}
	if cut >= 0 {
		isolate(cut, false)
	}

	// Settle: long enough for every backlog to drain after the heal. The
	// settle step is ordered after every step the nodes took so far, and
	// their later steps (the reads) after it.
	time.Sleep(3 * time.Second)
	c.mu.Lock()
	var nodes []*maelstrom.Node
	for _, id := range ids {
		nodes = append(nodes, c.nodes[id])
	}
	c.mu.Unlock()
	at := maelstrom.Barrier(nodes...)
	qobs.NewRecorder(sink, qobs.WithID("sim")).Record(context.Background(), "settle", nil,
		"actor", "cluster", "process", "cluster", "thread", "sim", "order.lamport", at)
	for _, id := range ids {
		_, _ = client.RPC(id, map[string]any{"type": "read"}, nil)
	}
	time.Sleep(time.Second)
}
