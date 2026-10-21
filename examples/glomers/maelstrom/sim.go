package maelstrom

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/casselc/quint-connect-go/qobs"
)

// Sim is a deterministic in-process network of nodes, services and
// clients. Nothing happens on its own: the caller (a scripted test, a
// seeded random schedule, or Hegel) says which in-flight message is
// delivered next (Deliver), which are lost (Drop), how the network is
// partitioned, and when the nodes' timers fire (Tick). Messages between
// partitioned nodes are lost when delivered, as under Maelstrom's
// partition nemesis. Clients and services are never partitioned.
type Sim struct {
	Nodes    map[string]*Node
	setup    func(n *Node)
	ids      []string
	pending  []Envelope
	services map[string]Service
	clients  map[int]func(Message)
	nextMsg  int
	groups   map[string]int // node -> partition group; nil: healed
	svcClock map[string]int64
	// Delivered, Lost: counts, for statistics and test assertions.
	Delivered, Lost int
}

// Envelope is a message in flight.
type Envelope struct {
	Msg Message
	Seq int // order in which it was sent
}

func (e Envelope) String() string {
	return fmt.Sprintf("#%d %s->%s %s", e.Seq, e.Msg.Src, e.Msg.Dest, e.Msg.Type())
}

// Service is a Maelstrom service (seq-kv, lin-kv, ...), answering requests
// through reply. step records a model step at the service: the moment a
// request takes effect (its linearization point), which the requesting
// node only learns of later, when the reply arrives.
type Service interface {
	Handle(m Message, reply func(body map[string]any), step func(action string, kv ...any))
}

type simTransport struct{ s *Sim }

func (t simTransport) Send(m Message) { t.s.enqueue(m) }

func (s *Sim) enqueue(m Message) {
	s.nextMsg++
	s.pending = append(s.pending, Envelope{Msg: m, Seq: s.nextMsg})
}

// NewSim starts nodes with the given ids, each set up by setup (register
// handlers, timers), and initialises them.
func NewSim(ids []string, setup func(n *Node)) *Sim {
	s := &Sim{Nodes: map[string]*Node{}, setup: setup, ids: append([]string(nil), ids...), services: map[string]Service{}, clients: map[int]func(Message){}, svcClock: map[string]int64{}}
	for _, id := range ids {
		n := NewNode()
		n.tr = simTransport{s}
		setup(n)
		s.Nodes[id] = n
	}
	for _, id := range ids {
		body, _ := json.Marshal(map[string]any{"type": "init", "msg_id": 1, "node_id": id, "node_ids": ids})
		_ = s.Nodes[id].Receive(Message{Src: "c0", Dest: id, Body: body})
	}
	s.pending = nil // the init_ok replies
	return s
}

// Restart replaces node id with a fresh one, as after a crash: its memory
// is gone (messages in flight to it still arrive, at the new node).
func (s *Sim) Restart(id string) {
	n := NewNode()
	n.tr = simTransport{s}
	s.setup(n)
	n.lamport = s.Nodes[id].lamport // the clock survives (a real node would read it from disk, or the next message)
	s.Nodes[id] = n
	body, _ := json.Marshal(map[string]any{"type": "init", "msg_id": 1, "node_id": id, "node_ids": s.ids})
	before := len(s.pending)
	_ = n.Receive(Message{Src: "c0", Dest: id, Body: body})
	s.pending = s.pending[:before] // drop init_ok
}

// Step records a model step of the harness itself (e.g. "settle"), ordered
// after every step the nodes have taken so far.
func (s *Sim) Step(action string, kv ...any) {
	var max int64
	for _, n := range s.Nodes {
		if n.lamport > max {
			max = n.lamport
		}
	}
	for _, n := range s.Nodes {
		n.lamport = max + 1 // later steps of the nodes come after this one
	}
	qobs.Record(context.Background(), action, nil, append(kv, "actor", "cluster", "process", "cluster", "thread", "sim", "order.lamport", max+1)...)
}

// IDs of the nodes.
func (s *Sim) IDs() []string { return append([]string(nil), s.ids...) }

// AddService makes a service (e.g. "seq-kv") reachable.
func (s *Sim) AddService(id string, svc Service) { s.services[id] = svc }

// Request sends body from client to node; cb gets the reply when it is
// delivered (it may never be, if lost).
func (s *Sim) Request(client, node string, body map[string]any, cb func(reply Message)) {
	s.nextMsg++
	id := s.nextMsg
	body["msg_id"] = id
	raw, _ := json.Marshal(body)
	s.clients[id] = cb
	s.enqueue(Message{Src: client, Dest: node, Body: raw})
}

// Pending returns the messages in flight, oldest first.
func (s *Sim) Pending() []Envelope { return append([]Envelope(nil), s.pending...) }

func (s *Sim) cut(a, b string) bool {
	if s.groups == nil {
		return false
	}
	ga, okA := s.groups[a]
	gb, okB := s.groups[b]
	return okA && okB && ga != gb
}

// Deliver delivers the i-th message in flight (lost if it crosses a
// partition). It returns false if there is no such message.
func (s *Sim) Deliver(i int) bool {
	if i < 0 || i >= len(s.pending) {
		return false
	}
	e := s.pending[i]
	s.pending = append(s.pending[:i], s.pending[i+1:]...)
	m := e.Msg
	if s.cut(m.Src, m.Dest) {
		s.Lost++
		return true
	}
	s.Delivered++
	if n, ok := s.Nodes[m.Dest]; ok {
		if err := n.Receive(m); err != nil {
			panic(fmt.Sprintf("node %s handling %s: %v", m.Dest, m.Type(), err))
		}
		return true
	}
	if svc, ok := s.services[m.Dest]; ok {
		if l := m.Header().Lamport; l > s.svcClock[m.Dest] {
			s.svcClock[m.Dest] = l
		}
		s.svcClock[m.Dest]++
		svc.Handle(m, func(body map[string]any) {
			body["in_reply_to"] = m.Header().MsgID
			s.svcClock[m.Dest]++
			body["lamport"] = s.svcClock[m.Dest]
			raw, _ := json.Marshal(body)
			s.enqueue(Message{Src: m.Dest, Dest: m.Src, Body: raw})
		}, func(action string, kv ...any) {
			s.svcClock[m.Dest]++
			qobs.Record(context.Background(), action, nil, append(kv, "actor", "cluster", "process", "cluster",
				"thread", m.Dest, "order.lamport", s.svcClock[m.Dest])...)
		})
		return true
	}
	// a client
	if cb, ok := s.clients[m.Header().InReplyTo]; ok {
		delete(s.clients, m.Header().InReplyTo)
		if cb != nil {
			cb(m)
		}
	}
	return true
}

// Drop loses the i-th message in flight.
func (s *Sim) Drop(i int) bool {
	if i < 0 || i >= len(s.pending) {
		return false
	}
	s.pending = append(s.pending[:i], s.pending[i+1:]...)
	s.Lost++
	return true
}

// Partition splits the nodes into groups; nodes not named are in a group
// of their own. Messages between groups are lost.
func (s *Sim) Partition(groups ...[]string) {
	s.groups = map[string]int{}
	for i, g := range groups {
		for _, n := range g {
			s.groups[n] = i
		}
	}
	for i, n := range s.ids {
		if _, ok := s.groups[n]; !ok {
			s.groups[n] = len(groups) + i
		}
	}
}

// Heal removes the partition.
func (s *Sim) Heal() { s.groups = nil }

// Partitioned reports whether a partition is in force.
func (s *Sim) Partitioned() bool { return s.groups != nil }

// Tick fires node id's timers.
func (s *Sim) Tick(id string) { s.Nodes[id].tick() }

// TickAll fires every node's timers.
func (s *Sim) TickAll() {
	for _, id := range s.ids {
		s.Tick(id)
	}
}

// Run delivers messages, choosing each with choose(len(pending)), until
// none is in flight or max deliveries. choose == nil delivers in order.
func (s *Sim) Run(choose func(n int) int, max int) int {
	k := 0
	for ; k < max && len(s.pending) > 0; k++ {
		i := 0
		if choose != nil {
			i = choose(len(s.pending))
		}
		s.Deliver(i)
	}
	return k
}

// Settle heals the network and lets the system quiesce: rounds of firing
// every timer and delivering everything in flight, in order. It returns
// false if messages are still in flight after the last round.
func (s *Sim) Settle(rounds int) bool {
	s.Heal()
	for r := 0; r < rounds; r++ {
		s.TickAll()
		s.Run(nil, 100000)
	}
	return len(s.pending) == 0
}

// String describes the network: partition and messages in flight.
func (s *Sim) String() string {
	var b strings.Builder
	if s.groups != nil {
		byG := map[int][]string{}
		for n, g := range s.groups {
			byG[g] = append(byG[g], n)
		}
		var gs []string
		for _, ns := range byG {
			sort.Strings(ns)
			gs = append(gs, "{"+strings.Join(ns, ",")+"}")
		}
		sort.Strings(gs)
		fmt.Fprintf(&b, "partition %s; ", strings.Join(gs, " "))
	}
	fmt.Fprintf(&b, "%d in flight", len(s.pending))
	return b.String()
}

// KV is Maelstrom's lin-kv: a linearizable key-value service with read,
// write, and cas with create_if_not_exists. (It is not a stand-in for
// seq-kv, which may let reads and updates act on past states.)
//
// With Name set, it records a model step for every operation on a key Name
// recognises, at the moment the operation takes effect: <kind>Read(node,
// key, value), <kind>Write(node, key, value), <kind>Cas(node, key, from,
// into, ok), where Name maps the store's key to (kind, model key). An
// absent key reads as Absent.
type KV struct {
	data   map[string]json.RawMessage
	Name   func(storeKey string) (kind, key string, ok bool)
	Absent func(kind string) any // what an absent key of this kind reads as, in the model
}

// NewKV returns an empty store.
func NewKV() *KV { return &KV{data: map[string]json.RawMessage{}} }

func decodeAny(raw json.RawMessage) any {
	if raw == nil {
		return nil
	}
	var v any
	_ = json.Unmarshal(raw, &v)
	return v
}

func (kv *KV) Handle(m Message, reply func(map[string]any), step func(string, ...any)) {
	var req struct {
		Type   string          `json:"type"`
		Key    any             `json:"key"`
		Value  json.RawMessage `json:"value"`
		From   json.RawMessage `json:"from"`
		To     json.RawMessage `json:"to"`
		Create bool            `json:"create_if_not_exists"`
	}
	_ = m.Decode(&req)
	k := fmt.Sprint(req.Key)
	record := func(op string, kvs ...any) {
		if kv.Name == nil {
			return
		}
		if kind, mk, ok := kv.Name(k); ok {
			step(kind+op, append([]any{"node", m.Src, "key", mk}, kvs...)...)
		}
	}
	switch req.Type {
	case "read":
		v, ok := kv.data[k]
		value := kv.Absent
		if ok {
			record("Read", "value", decodeAny(v))
		} else if value != nil && kv.Name != nil {
			if kind, _, named := kv.Name(k); named {
				record("Read", "value", value(kind))
			}
		}
		if !ok {
			reply(map[string]any{"type": "error", "code": KeyDoesNotExist, "text": "key does not exist"})
			return
		}
		reply(map[string]any{"type": "read_ok", "value": v})
	case "write":
		kv.data[k] = req.Value
		record("Write", "value", decodeAny(req.Value))
		reply(map[string]any{"type": "write_ok"})
	case "cas":
		v, ok := kv.data[k]
		switch {
		case !ok && !req.Create:
			record("Cas", "from", decodeAny(req.From), "into", decodeAny(req.To), "ok", false)
			reply(map[string]any{"type": "error", "code": KeyDoesNotExist, "text": "key does not exist"})
		case ok && string(v) != string(req.From):
			record("Cas", "from", decodeAny(req.From), "into", decodeAny(req.To), "ok", false)
			reply(map[string]any{"type": "error", "code": PreconditionFails, "text": fmt.Sprintf("expected %s, had %s", req.From, v)})
		default:
			kv.data[k] = req.To
			record("Cas", "from", decodeAny(req.From), "into", decodeAny(req.To), "ok", true)
			reply(map[string]any{"type": "cas_ok"})
		}
	default:
		reply(map[string]any{"type": "error", "code": 10, "text": "unsupported " + req.Type})
	}
}
