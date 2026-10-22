// Package pipeline is the application: a publisher that relays keyed
// events from a transactional outbox to a partitioned log, and consumers
// that read the log, apply each event to a projection and commit how far
// they got. It runs on Kafka (franz-go), on Kinesis (aws-sdk-go-v2) and
// on the simulated log (package simlog) through two seams, Writer and
// Reader, and a transactional store (qenv.DB) for the outbox and the
// projection.
//
// Model steps (../model/streams.qnt) are recorded where they take effect:
// publish when the publisher learns a record landed (and removes it from
// the outbox), read when a consumer receives a record, apply at the
// projection's commit (Txn.OnCommit). commit is recorded by the log where
// a commit lands (simlog) or, for the real clients, when the client is
// told it landed.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qobs"
)

// A Record is one record of the log, as a consumer reads it: its partition
// (Kafka partition, Kinesis shard by index) and offset (its index in the
// partition), and the event it carries: the key's N-th event.
type Record struct {
	Partition, Offset int
	Key               string
	N, Value          int
}

// Writer is the producer side of the log. Produce sends a record and
// calls done once with the outcome, nil when the log acknowledged it.
// Records of one key go to one partition, and are sent in the order
// produced; whether they land in that order after a retry is the client's
// business (Kafka's idempotent producer keeps it). done may run on
// another goroutine. Flush waits until every record produced is done.
type Writer interface {
	Produce(ctx context.Context, key string, n, value int, done func(error))
	Flush(ctx context.Context)
}

// Reader is one consumer's view of the log: it is assigned partitions
// (consumer group, leases), reads them from their committed offsets, and
// commits offsets. Commit(p, next) says that every record of p before next
// was processed; it may fail, and a consumer that lost the partition in a
// rebalance is refused (ErrFenced).
type Reader interface {
	Poll(ctx context.Context) ([]Record, error)
	Commit(ctx context.Context, partition, next int) error
}

// ErrFenced is a commit from a consumer that no longer owns the partition.
var ErrFenced = errors.New("pipeline: fenced: the partition was reassigned")

// Mutation selects a bug.
type Mutation int

const (
	Faithful Mutation = iota
	// CommitFirst commits a batch before applying it: a crash between the
	// two loses the batch (the restarted consumer resumes past it).
	CommitFirst
	// NoDedupe applies every record it reads: a redelivery (a lost commit,
	// a rebalance, a crash, a producer's duplicate) applies an event twice.
	NoDedupe
	// NonIdempotentProducer turns the producer's idempotence off but keeps
	// its retries and several requests in flight: a retried record lands
	// after the next one, and the key's order breaks. (A client setting:
	// the deployment applies it to the Writer.)
	NonIdempotentProducer
	// OffByOne starts its "last applied offset" at 0 instead of -1, so it
	// skips each partition's first record: a plain test sees it.
	OffByOne
)

func (m Mutation) String() string {
	return [...]string{"faithful", "commit-first", "no-dedupe", "non-idempotent-producer", "off-by-one"}[m]
}

// Outbox writes events 1..n of each key (value: n) to db's outbox, as
// the business transactions that raise them would.
func Outbox(ctx context.Context, db qenv.DB, keys []string, n int) error {
	return qenv.Retry(ctx, db, 5, func(tx qenv.Txn) error {
		for _, k := range keys {
			for i := 1; i <= n; i++ {
				tx.Put(outboxKey(k, i), i)
			}
		}
		return nil
	})
}

func outboxKey(key string, n int) string { return fmt.Sprintf("outbox/%s/%06d", key, n) }

// Publish relays the outbox to the log until it is empty: every row is
// produced, in key order, and removed once acknowledged, with publish
// recorded; rows the client gave up on are produced again. A restarted
// publisher resumes with what is left (rows acknowledged but not yet
// removed are written twice: the consumers deduplicate).
func Publish(ctx context.Context, w Writer, db qenv.DB) error {
	for {
		tx := db.Begin(ctx)
		rows := tx.Scan("outbox/")
		tx.Rollback()
		if len(rows) == 0 {
			return nil
		}
		var mu sync.Mutex // done runs on the client's goroutines
		acked := make([]bool, len(rows))
		for i, r := range rows {
			parts := strings.Split(r.Key, "/")
			n, _ := strconv.Atoi(parts[2])
			w.Produce(ctx, parts[1], n, r.Val.(int), func(err error) {
				mu.Lock()
				defer mu.Unlock()
				acked[i] = err == nil
			})
		}
		w.Flush(ctx)
		retry := false
		for i, r := range rows {
			if !acked[i] {
				retry = true
				continue
			}
			parts := strings.Split(r.Key, "/")
			n, _ := strconv.Atoi(parts[2])
			err := qenv.Retry(ctx, db, 5, func(tx qenv.Txn) error {
				tx.Delete(r.Key)
				tx.OnCommit(func() { qobs.Record(ctx, "publish", nil, "key", parts[1], "n", n) })
				return nil
			})
			if err != nil {
				return err
			}
		}
		if retry {
			qenv.Sleep(ctx, 100*time.Millisecond)
		}
	}
}

// Consumer applies what its Reader delivers to the projection in DB: per
// key, a version (the last n applied) and a total. Apply is idempotent by
// the version: an event at or below it is a redelivery.
type Consumer struct {
	Reader   Reader
	DB       qenv.DB
	Mutation Mutation
	last     map[int]int // OffByOne: the last offset seen per partition
}

// Run polls and processes batches until ctx ends.
func (c *Consumer) Run(ctx context.Context) {
	for ctx.Err() == nil {
		recs, err := c.Reader.Poll(ctx)
		if err != nil {
			qenv.Sleep(ctx, 50*time.Millisecond)
			continue
		}
		c.Process(ctx, recs)
	}
}

// Process handles one batch: read steps, applies, then a commit per
// partition of the offset after its last record.
func (c *Consumer) Process(ctx context.Context, recs []Record) {
	next := map[int]int{}
	for _, r := range recs {
		qobs.Record(ctx, "read", nil, "partition", r.Partition, "offset", r.Offset, "key", r.Key, "n", r.N)
		next[r.Partition] = r.Offset + 1
	}
	if c.Mutation == CommitFirst {
		c.commit(ctx, next)
	}
	for _, r := range recs {
		if c.Mutation == OffByOne {
			if c.last == nil {
				c.last = map[int]int{}
			}
			if r.Offset <= c.last[r.Partition] { // the bug: an unset entry reads 0, not -1
				continue
			}
			c.last[r.Partition] = r.Offset
		}
		if err := c.apply(ctx, r); err != nil {
			return // not committed: redelivered later
		}
	}
	if c.Mutation != CommitFirst {
		c.commit(ctx, next)
	}
}

func (c *Consumer) commit(ctx context.Context, next map[int]int) {
	ps := make([]int, 0, len(next))
	for p := range next {
		ps = append(ps, p)
	}
	sort.Ints(ps)
	for _, p := range ps {
		// a failed commit is covered by the next batch's, or redelivers
		_ = c.Reader.Commit(ctx, p, next[p])
	}
}

func (c *Consumer) apply(ctx context.Context, r Record) error {
	return qenv.Retry(ctx, c.DB, 20, func(tx qenv.Txn) error {
		ver := 0
		if v, ok := tx.Get("ver/" + r.Key); ok {
			ver = v.(int)
		}
		if c.Mutation != NoDedupe && r.N <= ver {
			return nil // a redelivery
		}
		total := 0
		if v, ok := tx.Get("total/" + r.Key); ok {
			total = v.(int)
		}
		tx.Put("ver/"+r.Key, max(ver, r.N))
		tx.Put("total/"+r.Key, total+r.Value)
		tx.OnCommit(func() { qobs.Record(ctx, "apply", nil, "key", r.Key, "n", r.N) })
		return nil
	})
}

// Total is the projection's total for key: the sum of the values applied.
func Total(ctx context.Context, db qenv.DB, key string) int {
	tx := db.Begin(ctx)
	defer tx.Rollback()
	if v, ok := tx.Get("total/" + key); ok {
		return v.(int)
	}
	return 0
}

// Version is the projection's version of key: the last n applied.
func Version(ctx context.Context, db qenv.DB, key string) int {
	tx := db.Begin(ctx)
	defer tx.Rollback()
	if v, ok := tx.Get("ver/" + key); ok {
		return v.(int)
	}
	return 0
}

// Keys are n keys "k1".."kn".
func Keys(n int) []string {
	ks := make([]string, n)
	for i := range ks {
		ks[i] = fmt.Sprintf("k%d", i+1)
	}
	return ks
}
