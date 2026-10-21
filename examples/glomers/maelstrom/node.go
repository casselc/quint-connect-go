// Package maelstrom is a small runtime for Maelstrom nodes
// (https://github.com/jepsen-io/maelstrom), the harness of Fly.io's Gossip
// Glomers challenges, with two things added for checking against Quint
// models:
//
//   - Step records a model step through qobs, stamped with the node as the
//     step's thread (program order) and a Lamport clock as an ordering key
//     (order.lamport). The clock travels in every message body ("lamport"),
//     so a receive is ordered after its send even when each node writes its
//     own step log, as under real Maelstrom;
//   - a deterministic in-process network (Sim) that runs nodes without
//     Maelstrom, where a test, or Hegel, chooses which message is delivered
//     next, which are lost, how the network is partitioned, and when timers
//     fire.
//
// The same node code runs under real Maelstrom with Run (stdin/stdout).
package maelstrom

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/casselc/quint-connect-go/qobs"
	"github.com/casselc/quint-connect-go/qtrace"
)

// Message is a Maelstrom message.
type Message struct {
	Src  string          `json:"src"`
	Dest string          `json:"dest"`
	Body json.RawMessage `json:"body"`
}

// Body holds the fields every message body has.
type Body struct {
	Type      string `json:"type"`
	MsgID     int    `json:"msg_id,omitempty"`
	InReplyTo int    `json:"in_reply_to,omitempty"`
	Code      int    `json:"code,omitempty"`
	Text      string `json:"text,omitempty"`
	Lamport   int64  `json:"lamport,omitempty"`
}

// Header decodes the common fields.
func (m Message) Header() Body {
	var b Body
	_ = json.Unmarshal(m.Body, &b)
	return b
}

// Type of the message.
func (m Message) Type() string { return m.Header().Type }

// Decode decodes the body into v.
func (m Message) Decode(v any) error { return json.Unmarshal(m.Body, v) }

// Error codes (Maelstrom's).
const (
	Timeout           = 0
	KeyDoesNotExist   = 20
	PreconditionFails = 22
)

// RPCError is an error reply.
type RPCError struct {
	Code int
	Text string
}

func (e *RPCError) Error() string { return fmt.Sprintf("maelstrom error %d: %s", e.Code, e.Text) }

// ErrorOf returns the error a reply carries, or nil.
func ErrorOf(m Message) error {
	h := m.Header()
	if h.Type != "error" {
		return nil
	}
	return &RPCError{Code: h.Code, Text: h.Text}
}

// Transport carries a node's outgoing messages.
type Transport interface {
	Send(m Message)
}

// Handler handles one message type.
type Handler func(m Message) error

// Node is one Maelstrom node.
type Node struct {
	mu        sync.Mutex
	id        string
	ids       []string
	handlers  map[string]Handler
	callbacks map[int]func(Message)
	nextID    int
	lamport   int64
	tr        Transport
	timers    []*timer
	onInit    []func()
	rec       *qobs.Recorder
}

type timer struct {
	every time.Duration
	f     func()
}

// NewNode returns a node; it learns its id from Maelstrom's init message.
func NewNode() *Node {
	return &Node{handlers: map[string]Handler{}, callbacks: map[int]func(Message){}}
}

// ID is the node's id ("n1"), once initialised.
func (n *Node) ID() string { return n.id }

// NodeIDs are all the nodes of the cluster.
func (n *Node) NodeIDs() []string { return append([]string(nil), n.ids...) }

// Handle registers the handler of a message type.
func (n *Node) Handle(typ string, h Handler) { n.handlers[typ] = h }

// OnInit runs f once the node knows its id (e.g. to open a step log).
func (n *Node) OnInit(f func()) { n.onInit = append(n.onInit, f) }

// Every runs f periodically: from a ticker under Run, when the network
// says so in a Sim (Sim.Tick). f runs with the node's lock held, like a
// handler.
func (n *Node) Every(d time.Duration, f func()) { n.timers = append(n.timers, &timer{every: d, f: f}) }

// SetRecorder sets the recorder of the node's model steps (default: the
// global one, qobs.SetGlobal).
func (n *Node) SetRecorder(r *qobs.Recorder) { n.rec = r }

// Step records a model step taken by this node: action, its error
// (outcome) and alternating keys and values, as qobs.Recorder.Record. The
// node is the step's thread, every node shares the process "cluster" and
// the actor "cluster", and the Lamport clock orders steps across nodes.
func (n *Node) Step(action string, err error, kv ...any) {
	n.lamport++
	kv = append(kv, "actor", "cluster", "process", "cluster", "thread", n.id, "order.lamport", n.lamport)
	if n.rec != nil {
		n.rec.Record(context.Background(), action, err, kv...)
	} else {
		qobs.Record(context.Background(), action, err, kv...)
	}
}

// stamp encodes body for dest, with msg_id when id > 0. Messages to other
// nodes carry the Lamport clock; Maelstrom rejects unknown keys in
// messages to its clients and services, so those do not.
func (n *Node) stamp(dest string, body any, id, inReplyTo int) (json.RawMessage, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("a message body must be a JSON object: %w", err)
	}
	n.lamport++
	if slices.Contains(n.ids, dest) {
		m["lamport"] = n.lamport
	}
	if id > 0 {
		m["msg_id"] = id
	}
	if inReplyTo > 0 {
		m["in_reply_to"] = inReplyTo
	}
	return json.Marshal(m)
}

// Send sends body to dest, expecting no reply.
func (n *Node) Send(dest string, body any) error {
	b, err := n.stamp(dest, body, 0, 0)
	if err != nil {
		return err
	}
	n.tr.Send(Message{Src: n.id, Dest: dest, Body: b})
	return nil
}

// Reply answers req with body.
func (n *Node) Reply(req Message, body any) error {
	b, err := n.stamp(req.Src, body, 0, req.Header().MsgID)
	if err != nil {
		return err
	}
	n.tr.Send(Message{Src: n.id, Dest: req.Src, Body: b})
	return nil
}

// ReplyError answers req with an error.
func (n *Node) ReplyError(req Message, code int, text string) error {
	return n.Reply(req, map[string]any{"type": "error", "code": code, "text": text})
}

// NextMsgID is the msg_id the next RPC will use, so a node can record the
// model step of a send before making it.
func (n *Node) NextMsgID() int { return n.nextID + 1 }

// RPC sends body to dest and calls cb with the reply, if one comes. It
// returns the message id. Callbacks run with the node's lock held.
func (n *Node) RPC(dest string, body any, cb func(reply Message)) (int, error) {
	n.nextID++
	id := n.nextID
	b, err := n.stamp(dest, body, id, 0)
	if err != nil {
		return 0, err
	}
	n.callbacks[id] = cb
	n.tr.Send(Message{Src: n.id, Dest: dest, Body: b})
	return id, nil
}

// Receive handles one incoming message: init, a reply to an RPC, or a
// request for a handler.
func (n *Node) Receive(m Message) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	h := m.Header()
	if h.Lamport > n.lamport {
		n.lamport = h.Lamport
	}
	n.lamport++
	if h.InReplyTo > 0 {
		if cb, ok := n.callbacks[h.InReplyTo]; ok {
			delete(n.callbacks, h.InReplyTo)
			if cb != nil {
				cb(m)
			}
		}
		return nil
	}
	if h.Type == "init" {
		var init struct {
			NodeID  string   `json:"node_id"`
			NodeIDs []string `json:"node_ids"`
		}
		if err := m.Decode(&init); err != nil {
			return err
		}
		n.id, n.ids = init.NodeID, init.NodeIDs
		for _, f := range n.onInit {
			f()
		}
		return n.Reply(m, map[string]any{"type": "init_ok"})
	}
	handler, ok := n.handlers[h.Type]
	if !ok {
		return n.ReplyError(m, 10, "unsupported message type "+h.Type)
	}
	return handler(m)
}

// tick runs the node's timers (every one, regardless of its period: the
// Sim decides when time passes).
func (n *Node) tick() {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, t := range n.timers {
		t.f()
	}
}

// stdio is the transport of a node run by Maelstrom.
type stdio struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *stdio) Send(m Message) {
	b, _ := json.Marshal(m)
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.w.Write(append(b, '\n'))
}

// Run serves the node under Maelstrom: messages on stdin, replies on
// stdout, timers on tickers. It returns when stdin closes.
func (n *Node) Run() error {
	n.tr = &stdio{w: os.Stdout}
	stop := make(chan struct{})
	defer close(stop)
	for _, t := range n.timers {
		go func(t *timer) {
			tk := time.NewTicker(t.every)
			defer tk.Stop()
			for {
				select {
				case <-stop:
					return
				case <-tk.C:
					if n.id != "" {
						n.mu.Lock()
						t.f()
						n.mu.Unlock()
					}
				}
			}
		}(t)
	}
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var m Message
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			return fmt.Errorf("bad message %q: %w", sc.Text(), err)
		}
		if err := n.Receive(m); err != nil {
			fmt.Fprintf(os.Stderr, "%s: handling %s: %v\n", n.id, m.Type(), err)
		}
	}
	return sc.Err()
}

// StepLogFromEnv opens a step log for a node run by Maelstrom, when
// $QUINT_STEPS_DIR is set: <dir>/<node id>.jsonl, one recorder per node.
// quintgo merges the nodes' logs, ordering them by the Lamport clock.
func StepLogFromEnv(n *Node) {
	dir := os.Getenv("QUINT_STEPS_DIR")
	if dir == "" {
		return
	}
	n.OnInit(func() {
		sink, err := qobs.FileSink(fmt.Sprintf("%s/%s.jsonl", dir, n.id))
		if err != nil {
			fmt.Fprintln(os.Stderr, "step log:", err)
			return
		}
		n.SetRecorder(qobs.NewRecorder(&flushing{sink}, qobs.WithID(n.id)))
	})
}

// flushing flushes after every step: Maelstrom kills nodes without warning.
type flushing struct{ *qobs.JSONLSink }

func (f *flushing) Record(ctx context.Context, s *qtrace.Step) {
	f.JSONLSink.Record(ctx, s)
	_ = f.Flush()
}
