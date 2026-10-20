// Package kv is the kvstore example app: a key-value service with puts,
// deletes, reads and compare-and-swap on a store-wide revision. Store is the
// state machine; Server (server.go) serves it over HTTP; Client is its
// client.
//
// The store reports each operation as a model step of model/kv.qnt through
// qobs, from inside the critical section that orders it, so the recorded
// order is the linearization order. It also stamps every operation with
// that order (Result.Op, the kv.op span attribute), which lets spans from
// concurrent requests be put back in order after the fact (kv.convert.yaml).
package kv

import (
	"context"
	"errors"
	"runtime"
	"sync"

	"github.com/casselc/quint-connect-go/qobs"
)

// Errors. ErrConflict and ErrNotFound classify themselves for the model
// (qobs.Outcomer): the binding maps "conflict" and "missing" to the model's
// Conflict and Missing outcomes.
var (
	ErrConflict = outcomeErr{"version conflict", "conflict"}
	ErrNotFound = outcomeErr{"key not found", "missing"}
)

type outcomeErr struct{ msg, outcome string }

func (e outcomeErr) Error() string        { return e.msg }
func (e outcomeErr) QuintOutcome() string { return e.outcome }

// Mutation injects a bug.
type Mutation int

const (
	Faithful Mutation = iota
	// StaleCAS checks the expected version under a read lock, releases it,
	// and writes under the write lock: two concurrent CAS with the same
	// expected version can both succeed. Only concurrency exposes it.
	StaleCAS
	// RevisionOnConflict advances the revision on a failed CAS, so the next
	// write gets a version the model does not expect. Any sequence of a
	// failed CAS and a write exposes it.
	RevisionOnConflict
)

// Entry is a stored value and the revision that wrote it.
type Entry struct {
	Value   string `json:"value"`
	Version int64  `json:"version"`
}

// Result describes a completed operation.
type Result struct {
	Revision int64 `json:"revision"` // the store's revision after the operation
	Op       int64 `json:"op"`       // linearization index: 1, 2, 3, ... per store
	Entry    Entry `json:"entry"`    // for reads: what was read
	Found    bool  `json:"found"`
}

// Store is a versioned key-value store, safe for concurrent use.
type Store struct {
	mu       sync.RWMutex
	data     map[string]Entry
	revision int64
	op       int64
	mutation Mutation
}

// NewStore returns an empty store.
func NewStore(m Mutation) *Store { return &Store{data: map[string]Entry{}, mutation: m} }

// record reports the model step; called with s.mu held.
func (s *Store) record(ctx context.Context, action string, err error, kv ...any) Result {
	s.op++
	qobs.Record(ctx, action, err, append(kv, "obs.revision", s.revision)...)
	return Result{Revision: s.revision, Op: s.op}
}

// Put stores value under key.
func (s *Store) Put(ctx context.Context, key, value string) Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revision++
	s.data[key] = Entry{Value: value, Version: s.revision}
	return s.record(ctx, "write", nil, "key", key, "value", value)
}

// CAS stores value under key if the key's current version is expected.
func (s *Store) CAS(ctx context.Context, key string, expected int64, value string) (Result, error) {
	if s.mutation == StaleCAS {
		s.mu.RLock()
		e, ok := s.data[key]
		s.mu.RUnlock()
		runtime.Gosched() // the window the bug lives in
		s.mu.Lock()
		defer s.mu.Unlock()
		if !ok || e.Version != expected {
			return s.record(ctx, "cas", ErrConflict, "key", key, "expected", expected, "value", value), ErrConflict
		}
		s.revision++
		s.data[key] = Entry{Value: value, Version: s.revision}
		return s.record(ctx, "cas", nil, "key", key, "expected", expected, "value", value), nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.data[key]; !ok || e.Version != expected {
		if s.mutation == RevisionOnConflict {
			s.revision++
		}
		return s.record(ctx, "cas", ErrConflict, "key", key, "expected", expected, "value", value), ErrConflict
	}
	s.revision++
	s.data[key] = Entry{Value: value, Version: s.revision}
	return s.record(ctx, "cas", nil, "key", key, "expected", expected, "value", value), nil
}

// Delete removes key.
func (s *Store) Delete(ctx context.Context, key string) (Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data[key]; !ok {
		return s.record(ctx, "del", ErrNotFound, "key", key), ErrNotFound
	}
	s.revision++
	delete(s.data, key)
	return s.record(ctx, "del", nil, "key", key), nil
}

// Get reads key. The read is a model step too: what it returned is checked
// against the model's store.
func (s *Store) Get(ctx context.Context, key string) (Result, error) {
	s.mu.Lock() // exclusive, so reads are linearized with writes and numbered
	defer s.mu.Unlock()
	e, ok := s.data[key]
	var err error
	if !ok {
		err = ErrNotFound
	}
	r := s.record(ctx, "read", nil, "key", key, "obs.found", ok, "obs.value", e.Value, "obs.version", e.Version)
	r.Entry, r.Found = e, ok
	return r, err
}

// Snapshot returns a copy of the data and the revision.
func (s *Store) Snapshot() (map[string]Entry, int64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]Entry, len(s.data))
	for k, v := range s.data {
		out[k] = v
	}
	return out, s.revision
}

// IsConflict and IsNotFound classify errors, including those a Client
// returns.
func IsConflict(err error) bool { return errors.Is(err, ErrConflict) }
func IsNotFound(err error) bool { return errors.Is(err, ErrNotFound) }
