// Package kafkalog runs the pipeline on Kafka with franz-go: Writer and
// Reader over kgo clients, and Run, a whole deployment against a broker
// (kfake's in-process cluster in the tests and the demo, any cluster
// otherwise).
package kafkalog

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/casselc/quint-connect-go/examples/streams/pipeline"
	"github.com/casselc/quint-connect-go/qobs"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
)

// Writer is a pipeline.Writer over a kgo producer. Records are keyed, so
// kgo's default partitioner sends a key to one partition; the value is
// "n:value".
type Writer struct {
	cl    *kgo.Client
	topic string
}

// NewWriter connects a producer. With idempotent false, the producer's
// idempotence is off (pipeline.NonIdempotentProducer): its retries can
// then reorder and duplicate.
func NewWriter(brokers []string, topic string, idempotent bool) (*Writer, error) {
	opts := []kgo.Opt{kgo.SeedBrokers(brokers...), kgo.DefaultProduceTopic(topic)}
	if !idempotent {
		opts = append(opts, kgo.DisableIdempotentWrite(), kgo.MaxProduceRequestsInflightPerBroker(5))
	}
	cl, err := kgo.NewClient(opts...)
	if err != nil {
		return nil, err
	}
	return &Writer{cl: cl, topic: topic}, nil
}

// Produce implements pipeline.Writer.
func (w *Writer) Produce(ctx context.Context, key string, n, value int, done func(error)) {
	r := &kgo.Record{Topic: w.topic, Key: []byte(key), Value: fmt.Appendf(nil, "%d:%d", n, value)}
	w.cl.Produce(ctx, r, func(_ *kgo.Record, err error) { done(err) })
}

// Flush implements pipeline.Writer.
func (w *Writer) Flush(ctx context.Context) { _ = w.cl.Flush(ctx) }

// Close closes the producer.
func (w *Writer) Close() { w.cl.Close() }

// Reader is a pipeline.Reader over a kgo consumer in a group, with
// automatic commits off: the pipeline commits after applying.
type Reader struct {
	cl    *kgo.Client
	topic string
}

// NewReader joins group as a consumer of topic.
func NewReader(brokers []string, topic, group string) (*Reader, error) {
	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers...), kgo.ConsumerGroup(group), kgo.ConsumeTopics(topic),
		kgo.DisableAutoCommit(), kgo.FetchMaxWait(100*time.Millisecond), kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
	if err != nil {
		return nil, err
	}
	return &Reader{cl: cl, topic: topic}, nil
}

// Poll implements pipeline.Reader: the records fetched within 200 ms.
func (r *Reader) Poll(ctx context.Context) ([]pipeline.Record, error) {
	pctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	fs := r.cl.PollRecords(pctx, 10)
	if fs.IsClientClosed() {
		return nil, kgo.ErrClientClosed
	}
	var out []pipeline.Record
	var bad error
	fs.EachRecord(func(rec *kgo.Record) {
		n, v, err := decode(rec.Value)
		if err != nil {
			bad = err
			return
		}
		out = append(out, pipeline.Record{Partition: int(rec.Partition), Offset: int(rec.Offset), Key: string(rec.Key), N: n, Value: v})
	})
	return out, bad
}

func decode(b []byte) (n, v int, err error) {
	a, c, ok := strings.Cut(string(b), ":")
	if !ok {
		return 0, 0, fmt.Errorf("kafkalog: bad value %q", b)
	}
	if n, err = strconv.Atoi(a); err == nil {
		v, err = strconv.Atoi(c)
	}
	return n, v, err
}

// Commit implements pipeline.Reader, recording the commit when the
// coordinator accepted it. A member of an older generation is refused
// (Kafka's ILLEGAL_GENERATION, REBALANCE_IN_PROGRESS).
func (r *Reader) Commit(ctx context.Context, partition, next int) error {
	var cerr error
	offsets := map[string]map[int32]kgo.EpochOffset{r.topic: {int32(partition): {Epoch: -1, Offset: int64(next)}}}
	r.cl.CommitOffsetsSync(ctx, offsets, func(_ *kgo.Client, _ *kmsg.OffsetCommitRequest, resp *kmsg.OffsetCommitResponse, err error) {
		if err != nil {
			cerr = err
			return
		}
		for _, t := range resp.Topics {
			for _, p := range t.Partitions {
				if e := kerr.ErrorForCode(p.ErrorCode); e != nil {
					cerr = e
				}
			}
		}
	})
	if cerr != nil {
		return cerr
	}
	qobs.Record(ctx, "commit", nil, "partition", partition, "offset", next)
	return nil
}

// Close leaves the group.
func (r *Reader) Close() { r.cl.Close() }
