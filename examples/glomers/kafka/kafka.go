// Package kafka solves Gossip Glomers challenge 5 (5b, 5c): a replicated,
// Kafka-style log on Maelstrom's lin-kv. Each key's log is one list in
// lin-kv ("log/<key>"); a send appends with a compare-and-swap, retrying
// on conflict, and the message's offset is its position. Committed offsets
// live in lin-kv too ("commit/<key>"), advanced with compare-and-swap so
// they never move back.
//
// The nodes record sendOk, pollOk and listOk when they answer clients;
// lin-kv records its operations on log/ and commit/ keys (see KV and
// ../model/kafka.qnt).
package kafka

import (
	"sort"
	"strings"

	"github.com/casselc/quint-connect-go/examples/glomers/maelstrom"
)

// Service is the KV service the nodes use.
const Service = "lin-kv"

// Mutation injects a bug.
type Mutation int

const (
	Faithful Mutation = iota
	// BlindAppend writes the extended list without a CAS: concurrent sends
	// overwrite each other, and an acknowledged message disappears.
	BlindAppend
	// PollSkips returns messages from one past the requested offset.
	PollSkips
)

// Setup registers the handlers.
func Setup(n *maelstrom.Node, mut Mutation) {
	readLog := func(key string, then func(log []int)) {
		_, _ = n.RPC(Service, map[string]any{"type": "read", "key": "log/" + key}, func(r maelstrom.Message) {
			var b struct {
				Value []int `json:"value"`
			}
			_ = r.Decode(&b) // absent: empty
			then(b.Value)
		})
	}
	var send func(key string, msg int, done func(offset int))
	send = func(key string, msg int, done func(int)) {
		readLog(key, func(log []int) {
			next := append(append([]int{}, log...), msg)
			if log == nil {
				log = []int{}
			}
			if mut == BlindAppend {
				_, _ = n.RPC(Service, map[string]any{"type": "write", "key": "log/" + key, "value": next},
					func(maelstrom.Message) { done(len(log)) })
				return
			}
			_, _ = n.RPC(Service, map[string]any{"type": "cas", "key": "log/" + key, "from": log, "to": next, "create_if_not_exists": true},
				func(r maelstrom.Message) {
					if maelstrom.ErrorOf(r) != nil {
						send(key, msg, done) // another send appended first
						return
					}
					done(len(log))
				})
		})
	}
	n.Handle("send", func(req maelstrom.Message) error {
		var b struct {
			Key string `json:"key"`
			Msg int    `json:"msg"`
		}
		if err := req.Decode(&b); err != nil {
			return err
		}
		send(b.Key, b.Msg, func(offset int) {
			n.Step("sendOk", nil, "node", n.ID(), "key", b.Key, "msg", b.Msg, "offset", offset)
			_ = n.Reply(req, map[string]any{"type": "send_ok", "offset": offset})
		})
		return nil
	})
	n.Handle("poll", func(req maelstrom.Message) error {
		var b struct {
			Offsets map[string]int `json:"offsets"`
		}
		if err := req.Decode(&b); err != nil {
			return err
		}
		keys := sortedKeys(b.Offsets)
		out := map[string][][2]int{}
		var next func(i int)
		next = func(i int) {
			if i == len(keys) {
				_ = n.Reply(req, map[string]any{"type": "poll_ok", "msgs": out})
				return
			}
			k, from := keys[i], b.Offsets[keys[i]]
			readLog(k, func(log []int) {
				start := from
				if mut == PollSkips {
					start++
				}
				msgs := [][2]int{}
				for o := start; o < len(log) && o < start+10; o++ {
					msgs = append(msgs, [2]int{o, log[o]})
				}
				out[k] = msgs
				n.Step("pollOk", nil, "node", n.ID(), "key", k, "from", from, "obs.msgs", msgs)
				next(i + 1)
			})
		}
		next(0)
		return nil
	})
	var commit, advance func(key string, offset int, done func())
	// commit ignores offsets past the end of the log: a client can only
	// have processed messages that exist. (Hegel found this: Maelstrom's
	// workload only commits offsets it polled, but nothing in the protocol
	// says so; the model's commitsInLog does.) A log only grows, so the
	// length read here still bounds it when the CAS lands.
	commit = func(key string, offset int, done func()) {
		readLog(key, func(log []int) {
			if offset >= len(log) {
				done()
				return
			}
			advance(key, offset, done)
		})
	}
	advance = func(key string, offset int, done func()) {
		_, _ = n.RPC(Service, map[string]any{"type": "read", "key": "commit/" + key}, func(r maelstrom.Message) {
			cur := -1
			if maelstrom.ErrorOf(r) == nil {
				var b struct {
					Value int `json:"value"`
				}
				_ = r.Decode(&b)
				cur = b.Value
			}
			if offset <= cur {
				done() // never move a commit back
				return
			}
			_, _ = n.RPC(Service, map[string]any{"type": "cas", "key": "commit/" + key, "from": cur, "to": offset, "create_if_not_exists": true},
				func(r maelstrom.Message) {
					if maelstrom.ErrorOf(r) != nil {
						advance(key, offset, done)
						return
					}
					done()
				})
		})
	}
	n.Handle("commit_offsets", func(req maelstrom.Message) error {
		var b struct {
			Offsets map[string]int `json:"offsets"`
		}
		if err := req.Decode(&b); err != nil {
			return err
		}
		keys := sortedKeys(b.Offsets)
		var next func(i int)
		next = func(i int) {
			if i == len(keys) {
				_ = n.Reply(req, map[string]any{"type": "commit_offsets_ok"})
				return
			}
			commit(keys[i], b.Offsets[keys[i]], func() { next(i + 1) })
		}
		next(0)
		return nil
	})
	n.Handle("list_committed_offsets", func(req maelstrom.Message) error {
		var b struct {
			Keys []string `json:"keys"`
		}
		if err := req.Decode(&b); err != nil {
			return err
		}
		out := map[string]int{}
		var next func(i int)
		next = func(i int) {
			if i == len(b.Keys) {
				_ = n.Reply(req, map[string]any{"type": "list_committed_offsets_ok", "offsets": out})
				return
			}
			k := b.Keys[i]
			_, _ = n.RPC(Service, map[string]any{"type": "read", "key": "commit/" + k}, func(r maelstrom.Message) {
				if maelstrom.ErrorOf(r) == nil {
					var v struct {
						Value int `json:"value"`
					}
					_ = r.Decode(&v)
					out[k] = v.Value
					n.Step("listOk", nil, "node", n.ID(), "key", k, "obs.offset", v.Value)
				}
				next(i + 1)
			})
		}
		next(0)
		return nil
	})
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// KV returns lin-kv for a Sim, recording the log and commit operations.
func KV() *maelstrom.KV {
	kv := maelstrom.NewKV()
	kv.Name = func(k string) (string, string, bool) {
		kind, key, ok := strings.Cut(k, "/")
		return kind, key, ok && (kind == "log" || kind == "commit")
	}
	kv.Absent = func(kind string) any {
		if kind == "log" {
			return []int{}
		}
		return -1
	}
	return kv
}
