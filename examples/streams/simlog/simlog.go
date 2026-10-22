// Package simlog is a partitioned log in a qenv simulation: Kafka's
// contract (Kinesis's is a subset), as the pipeline's Writer and Reader
// see it. Its contract allows, and its faults produce:
//   - a produce request lost before it lands, or landed with its
//     acknowledgement lost ("fault.log.produce": lost, lostAck): the
//     producer client retries, several requests in flight per partition.
//     With Idempotent (Kafka's default), the log takes a producer's
//     records of a partition in sequence and drops its duplicates, so
//     retries keep the order; without, a retried record lands after the
//     ones sent behind it, and a lost acknowledgement writes it twice;
//   - a commit lost, or landed with its acknowledgement lost
//     ("fault.log.commit": lost, lostAck);
//   - a fetch that goes back to the committed offset ("fault.log.fetch":
//     rewind): a duplicate delivery, as after a fetch-session reset;
//   - a consumer evicted from its group at any step ("rebalance.<name>",
//     a nemesis, as a session timeout does): the group's generation moves
//     on, the partitions are reassigned and read again from their
//     committed offsets, and the evicted consumer's commits are fenced
//     until it rejoins at its next poll. A consumer whose process crashes
//     leaves the group the same way.
//
// Partitions are read in offset order with no holes, and a record never
// changes: what the model assumes of any log.
package simlog

import (
	"context"
	"errors"
	"hash/fnv"
	"sort"
	"time"

	"github.com/casselc/quint-connect-go/examples/streams/pipeline"
	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qobs"
)

var (
	produceFault = qenv.DeclareFaults("log.produce", "lost", "lostAck")
	commitFault  = qenv.DeclareFaults("log.commit", "lost", "lostAck")
	fetchFault   = qenv.DeclareFaults("log.fetch", "rewind")
)

var (
	// ErrLost is a request that never reached the log.
	ErrLost = errors.New("simlog: request lost")
	// ErrLostAck is a request that took effect, its acknowledgement lost.
	ErrLostAck = errors.New("simlog: acknowledgement lost")
	// ErrOutOfOrder is an idempotent producer's record sent ahead of an
	// earlier one that has not landed: retried after it.
	ErrOutOfOrder = errors.New("simlog: out of order sequence")
)

// Log is one topic and its one consumer group.
type Log struct {
	Name string
	// Batch is the most records a poll returns per partition (default 2).
	Batch int

	sim       *qenv.Sim
	parts     [][]pipeline.Record
	committed map[int]int
	seqs      map[int]map[int]int // producer -> partition -> last sequence taken
	producers int
	members   []*Member // the group, by name
	gen       int
	byName    map[string]*Member // each consumer name's current member
}

// New returns an empty log of n partitions in s.
func New(s *qenv.Sim, name string, n int) *Log {
	l := &Log{Name: name, Batch: 2, sim: s, parts: make([][]pipeline.Record, n), committed: map[int]int{},
		seqs: map[int]map[int]int{}, byName: map[string]*Member{}}
	s.OnCrash(func(p *qenv.Process) {
		for _, m := range l.members {
			if m.proc == p {
				l.leave(m)
			}
		}
	})
	return l
}

// Partition is the partition of key: a hash, as Kafka's and Kinesis's
// clients choose one.
func (l *Log) Partition(key string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return int(h.Sum32() % uint32(len(l.parts)))
}

// Records are partition p's records.
func (l *Log) Records(p int) []pipeline.Record { return l.parts[p] }

// append lands a producer's record, by Kafka's idempotence rule if asked.
func (l *Log) append(producer, pseq int, idempotent bool, r pipeline.Record) error {
	p := r.Partition
	if idempotent {
		last := l.seqs[producer][p]
		switch {
		case pseq <= last:
			return nil // a duplicate: acknowledged, not written again
		case pseq > last+1:
			return ErrOutOfOrder
		}
		if l.seqs[producer] == nil {
			l.seqs[producer] = map[int]int{}
		}
		l.seqs[producer][p] = pseq
	}
	r.Offset = len(l.parts[p])
	l.parts[p] = append(l.parts[p], r)
	return nil
}

// ---- producing ----

// Producer is a producer client of the log: records are sent per
// partition in the order produced, InFlight requests at a time (default
// 5, Kafka's), each retried until it lands.
type Producer struct {
	Idempotent bool
	InFlight   int

	log         *Log
	id          int
	unsent      map[int][]*pending // per partition, in order
	pseq        map[int]int
	outstanding int
	sending     bool
}

type pending struct {
	rec  pipeline.Record
	pseq int
	done func(error)
}

// Producer returns a new producer client (a new producer id: a restarted
// process's client does not inherit its predecessor's sequences).
func (l *Log) Producer(idempotent bool) *Producer {
	l.producers++
	return &Producer{Idempotent: idempotent, InFlight: 5, log: l, id: l.producers, unsent: map[int][]*pending{}, pseq: map[int]int{}}
}

// Produce implements pipeline.Writer.
func (p *Producer) Produce(ctx context.Context, key string, n, value int, done func(error)) {
	part := p.log.Partition(key)
	p.pseq[part]++
	p.unsent[part] = append(p.unsent[part], &pending{pipeline.Record{Partition: part, Key: key, N: n, Value: value}, p.pseq[part], done})
	p.outstanding++
	if !p.sending {
		p.sending = true
		qenv.Go(ctx, "producer.sender", p.send)
	}
}

// Flush implements pipeline.Writer.
func (p *Producer) Flush(ctx context.Context) {
	qenv.Wait(ctx, func() bool { return p.outstanding == 0 })
}

// send sends each partition's first InFlight records, one request each,
// in order; those that failed are retried, still in order.
func (p *Producer) send(ctx context.Context) {
	sim := p.log.sim
	qenv.Daemon(ctx) // the producer's sender waits for records forever
	for {
		qenv.Wait(ctx, func() bool { return p.outstanding > 0 })
		parts := make([]int, 0, len(p.unsent))
		for part, q := range p.unsent {
			if len(q) > 0 {
				parts = append(parts, part)
			}
		}
		sort.Ints(parts)
		retry := false
		for _, part := range parts {
			q := p.unsent[part]
			batch := q[:min(len(q), p.InFlight)]
			var keep []*pending
			for _, r := range batch {
				qenv.Yield(ctx)
				var err error
				switch sim.FaultOf(produceFault) {
				case "lost":
					err = ErrLost
				case "lostAck":
					if err = p.log.append(p.id, r.pseq, p.Idempotent, r.rec); err == nil {
						err = ErrLostAck
					}
				default:
					err = p.log.append(p.id, r.pseq, p.Idempotent, r.rec)
				}
				if err != nil {
					keep = append(keep, r)
					continue
				}
				p.outstanding--
				r.done(nil)
			}
			p.unsent[part] = append(keep, q[len(batch):]...)
			retry = retry || len(keep) > 0
		}
		if retry {
			qenv.Sleep(ctx, 20*time.Millisecond)
		}
	}
}

// ---- consuming ----

// Member is one consumer of the group, as one process incarnation sees
// it. It joins at its first poll.
type Member struct {
	Name string

	log  *Log
	proc *qenv.Process
	in   bool
	gen  int         // the generation its assignment and positions are of
	pos  map[int]int // next offset to fetch, per assigned partition
}

// Member returns the consumer name's member for the calling process.
// Each name can be evicted at any step (the "rebalance.<name>" nemesis).
func (l *Log) Member(ctx context.Context, name string) *Member {
	m := &Member{Name: name, log: l, proc: qenv.ProcessOf(ctx)}
	if _, ok := l.byName[name]; !ok {
		l.sim.Nemesis("rebalance."+name, func() bool { m := l.byName[name]; return m != nil && m.in }, func() { l.leave(l.byName[name]) })
	}
	l.byName[name] = m
	return m
}

func (l *Log) join(m *Member) {
	m.in = true
	l.members = append(l.members, m)
	sort.Slice(l.members, func(i, j int) bool { return l.members[i].Name < l.members[j].Name })
	l.gen++
}

func (l *Log) leave(m *Member) {
	for i, x := range l.members {
		if x == m {
			l.members = append(l.members[:i:i], l.members[i+1:]...)
			m.in = false
			l.gen++
			return
		}
	}
}

// assigned are m's partitions in the current generation: round robin over
// the members by name.
func (l *Log) assigned(m *Member) []int {
	var ps []int
	for p := range l.parts {
		if len(l.members) > 0 && l.members[p%len(l.members)] == m {
			ps = append(ps, p)
		}
	}
	return ps
}

func (m *Member) ready() bool {
	l := m.log
	if !m.in || m.gen != l.gen {
		return true
	}
	for p, o := range m.pos {
		if o < len(l.parts[p]) {
			return true
		}
	}
	return false
}

// Poll implements pipeline.Reader: it waits for records of its
// partitions, rejoining the group first if it was evicted, and reading
// from the committed offsets after a rebalance (an eager one: every
// partition is revoked and assigned again).
func (m *Member) Poll(ctx context.Context) ([]pipeline.Record, error) {
	l := m.log
	qenv.WaitIdle(ctx, m.ready)
	if !m.in {
		l.join(m)
	}
	if m.gen != l.gen {
		m.gen, m.pos = l.gen, map[int]int{}
		for _, p := range l.assigned(m) {
			m.pos[p] = l.committed[p]
		}
	}
	if l.sim.FaultOf(fetchFault) == "rewind" {
		for p := range m.pos {
			m.pos[p] = l.committed[p]
		}
	}
	var out []pipeline.Record
	ps := make([]int, 0, len(m.pos))
	for p := range m.pos {
		ps = append(ps, p)
	}
	sort.Ints(ps)
	for _, p := range ps {
		recs := l.parts[p][m.pos[p]:]
		recs = recs[:min(len(recs), l.Batch)]
		out = append(out, recs...)
		m.pos[p] += len(recs)
	}
	return out, nil
}

// Commit implements pipeline.Reader. A commit that lands is the model's
// commit step, recorded here.
func (m *Member) Commit(ctx context.Context, partition, next int) error {
	l := m.log
	qenv.Yield(ctx)
	if !m.in || m.gen != l.gen {
		return pipeline.ErrFenced
	}
	if _, ok := m.pos[partition]; !ok {
		return pipeline.ErrFenced
	}
	f := l.sim.FaultOf(commitFault)
	if f == "lost" {
		return ErrLost
	}
	l.committed[partition] = next
	qobs.Record(ctx, "commit", nil, "partition", partition, "offset", next)
	if f == "lostAck" {
		return ErrLostAck
	}
	return nil
}

// Committed is the group's committed offset of partition p.
func (l *Log) Committed(p int) int { return l.committed[p] }
