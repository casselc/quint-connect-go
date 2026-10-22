// Package tcp carries Maelstrom messages between nodes over TCP: one JSON
// message per line, one connection per destination, dialled on demand.
// It is ordinary networking code with nothing simulation-specific in it;
// under gosim it runs on gosim's simulated network.
package tcp

import (
	"bufio"
	"encoding/json"
	"net"
	"sync"
	"time"

	"github.com/casselc/quint-connect-go/examples/glomers/maelstrom"
)

// Transport sends a node's messages. Like Maelstrom's network it may lose
// them: a message to an unreachable node, or one that does not fit in the
// destination's queue, is dropped.
type Transport struct {
	// Addr is the host:port of a node or client id; "" if unknown.
	Addr func(id string) string
	// Timeout bounds a dial and a write (default 1s). Without it, a write
	// to a peer behind a partition can block its sender for good.
	Timeout time.Duration

	mu  sync.Mutex
	out map[string]chan maelstrom.Message
}

// Send queues m for its destination.
func (t *Transport) Send(m maelstrom.Message) {
	t.mu.Lock()
	if t.out == nil {
		t.out = map[string]chan maelstrom.Message{}
	}
	ch, ok := t.out[m.Dest]
	if !ok {
		addr := t.Addr(m.Dest)
		if addr == "" {
			t.mu.Unlock()
			return
		}
		ch = make(chan maelstrom.Message, 256)
		t.out[m.Dest] = ch
		go t.send(addr, ch)
	}
	t.mu.Unlock()
	select {
	case ch <- m:
	default: // queue full: lost
	}
}

func (t *Transport) timeout() time.Duration {
	if t.Timeout == 0 {
		return time.Second
	}
	return t.Timeout
}

// send writes the messages of one destination, redialling after an error.
// A message that fails to go out is dropped: the protocol above resends.
func (t *Transport) send(addr string, ch chan maelstrom.Message) {
	var conn net.Conn
	for m := range ch {
		if conn == nil {
			c, err := net.DialTimeout("tcp", addr, t.timeout())
			if err != nil {
				continue
			}
			conn = c
		}
		b, _ := json.Marshal(m)
		conn.SetWriteDeadline(time.Now().Add(t.timeout()))
		if _, err := conn.Write(append(b, '\n')); err != nil {
			conn.Close()
			conn = nil
		}
	}
}

// Serve accepts connections on ln and passes their messages to n until ln
// is closed.
func Serve(ln net.Listener, n *maelstrom.Node) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go func() {
			defer conn.Close()
			sc := bufio.NewScanner(conn)
			sc.Buffer(make([]byte, 64<<10), 16<<20)
			for sc.Scan() {
				var m maelstrom.Message
				if json.Unmarshal(sc.Bytes(), &m) == nil {
					_ = n.Receive(m)
				}
			}
		}()
	}
}
