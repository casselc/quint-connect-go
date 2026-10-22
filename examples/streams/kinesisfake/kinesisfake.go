// Package kinesisfake is an in-process Kinesis endpoint for the
// pipeline's Kinesis tier: the four data-plane calls it makes
// (PutRecord, ListShards, GetShardIterator, GetRecords), in the
// awsJson1_1 protocol the AWS SDK speaks, over an httptest server. A
// partition key goes to the shard whose hash-key range holds its MD5, as
// in Kinesis; sequence numbers increase within a shard. It holds no
// retention, throughput limits, resharding or enhanced fan-out.
package kinesisfake

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Server is a fake Kinesis with one stream.
type Server struct {
	*httptest.Server
	Stream string

	mu     sync.Mutex
	shards [][]record
}

type record struct {
	Seq, Key string
	Data     []byte
	At       time.Time
}

// New starts a server with stream of n shards. URL is its endpoint.
func New(stream string, n int) *Server {
	s := &Server{Stream: stream, shards: make([][]record, n)}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

func shardID(i int) string { return fmt.Sprintf("shardId-%012d", i) }

var space = new(big.Int).Lsh(big.NewInt(1), 128) // the hash-key space

func (s *Server) rangeOf(i int) (lo, hi *big.Int) {
	n := big.NewInt(int64(len(s.shards)))
	lo = new(big.Int).Div(new(big.Int).Mul(space, big.NewInt(int64(i))), n)
	hi = new(big.Int).Sub(new(big.Int).Div(new(big.Int).Mul(space, big.NewInt(int64(i+1))), n), big.NewInt(1))
	return lo, hi
}

func (s *Server) shardOf(key string) int {
	h := md5.Sum([]byte(key))
	v := new(big.Int).SetBytes(h[:])
	for i := range s.shards {
		if _, hi := s.rangeOf(i); v.Cmp(hi) <= 0 {
			return i
		}
	}
	return len(s.shards) - 1
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	op := strings.TrimPrefix(r.Header.Get("X-Amz-Target"), "Kinesis_20131202.")
	var in map[string]any
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, "SerializationException", err.Error())
		return
	}
	str := func(k string) string { v, _ := in[k].(string); return v }
	if name := str("StreamName"); name != "" && name != s.Stream {
		fail(w, "ResourceNotFoundException", "Stream "+name+" not found")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch op {
	case "PutRecord":
		data, err := base64.StdEncoding.DecodeString(str("Data"))
		if err != nil {
			fail(w, "SerializationException", err.Error())
			return
		}
		i := s.shardOf(str("PartitionKey"))
		seq := fmt.Sprintf("%028d%028d", i+1, len(s.shards[i])+1)
		s.shards[i] = append(s.shards[i], record{Seq: seq, Key: str("PartitionKey"), Data: data, At: time.Now()})
		reply(w, map[string]any{"ShardId": shardID(i), "SequenceNumber": seq})
	case "ListShards":
		var out []map[string]any
		for i := range s.shards {
			lo, hi := s.rangeOf(i)
			out = append(out, map[string]any{"ShardId": shardID(i),
				"HashKeyRange":        map[string]string{"StartingHashKey": lo.String(), "EndingHashKey": hi.String()},
				"SequenceNumberRange": map[string]string{"StartingSequenceNumber": fmt.Sprintf("%028d%028d", i+1, 0)}})
		}
		reply(w, map[string]any{"Shards": out})
	case "GetShardIterator":
		i, ok := s.shardIndex(str("ShardId"))
		if !ok {
			fail(w, "ResourceNotFoundException", "no shard "+str("ShardId"))
			return
		}
		pos := 0
		switch str("ShardIteratorType") {
		case "TRIM_HORIZON":
		case "LATEST":
			pos = len(s.shards[i])
		case "AT_SEQUENCE_NUMBER", "AFTER_SEQUENCE_NUMBER":
			pos = len(s.shards[i])
			for j, rec := range s.shards[i] {
				if rec.Seq == str("StartingSequenceNumber") {
					pos = j
					if str("ShardIteratorType") == "AFTER_SEQUENCE_NUMBER" {
						pos++
					}
				}
			}
		default:
			fail(w, "InvalidArgumentException", "iterator type "+str("ShardIteratorType"))
			return
		}
		reply(w, map[string]any{"ShardIterator": iterator(i, pos)})
	case "GetRecords":
		i, pos, ok := parseIterator(str("ShardIterator"))
		if !ok || i >= len(s.shards) {
			fail(w, "ExpiredIteratorException", "bad iterator")
			return
		}
		limit := 10000
		if l, ok := in["Limit"].(float64); ok && l > 0 {
			limit = int(l)
		}
		recs := s.shards[i][min(pos, len(s.shards[i])):]
		recs = recs[:min(len(recs), limit)]
		out := []map[string]any{}
		for _, rec := range recs {
			out = append(out, map[string]any{"SequenceNumber": rec.Seq, "PartitionKey": rec.Key,
				"Data": base64.StdEncoding.EncodeToString(rec.Data), "ApproximateArrivalTimestamp": float64(rec.At.UnixMilli()) / 1000})
		}
		reply(w, map[string]any{"Records": out, "NextShardIterator": iterator(i, pos+len(recs)),
			"MillisBehindLatest": 0})
	default:
		fail(w, "UnknownOperationException", "kinesisfake does not implement "+op)
	}
}

func (s *Server) shardIndex(id string) (int, bool) {
	for i := range s.shards {
		if shardID(i) == id {
			return i, true
		}
	}
	return 0, false
}

func iterator(shard, pos int) string {
	return base64.StdEncoding.EncodeToString(fmt.Appendf(nil, "%d:%d", shard, pos))
}

func parseIterator(it string) (shard, pos int, ok bool) {
	b, err := base64.StdEncoding.DecodeString(it)
	if err != nil {
		return 0, 0, false
	}
	a, c, found := strings.Cut(string(b), ":")
	shard, err1 := strconv.Atoi(a)
	pos, err2 := strconv.Atoi(c)
	return shard, pos, found && err1 == nil && err2 == nil
}

func reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, typ, msg string) {
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]string{"__type": typ, "message": msg})
}
