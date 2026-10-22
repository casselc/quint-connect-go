// Package store has no coverage code of its own: qweave -cover adds it.
package store

import (
	"context"
	"errors"

	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qobs"
)

// ErrFull is the store's fault.
var ErrFull = errors.New("full")

// ErrMissing is a get's error.
var ErrMissing = errors.New("missing")

// Store is a map.
type Store struct {
	m       map[string]string
	missing map[string]error
	closed  bool
}

// New returns an empty store.
func New() *Store {
	return &Store{m: map[string]string{}, missing: map[string]error{"absent": ErrMissing}}
}

// Put writes k, or fails at its fault point.
func (s *Store) Put(ctx context.Context, k, v string) error {
	if err := qenv.FaultPoint(ctx, "store.put", false, ErrFull); err != nil {
		qobs.Record(ctx, "put", err, "key", k)
		return err
	}
	s.m[k] = v
	qobs.Record(ctx, "put", nil, "key", k)
	return nil
}

// Get records its outcome without branching on it: its ok and error steps
// run the same code.
func (s *Store) Get(ctx context.Context, k string) string {
	err := s.missing[k]
	qobs.Record(ctx, "get", err, "key", k)
	return s.m[k]
}

// Close runs after the last step: code no step accounts for.
func (s *Store) Close() { s.closed = true }

// Compact is never called.
func (s *Store) Compact() {
	for k := range s.m {
		delete(s.m, k)
	}
}

// Kind has a switch, a type switch and a select: their clauses are
// blocks, their bodies are not.
func Kind(v any, ch chan int) string {
	switch x := v.(type) {
	case int:
		if x > 0 {
			return "positive"
		}
	case string:
		return "string"
	}
	switch {
	case v == nil:
		return "nil"
	default:
	}
	select {
	case <-ch:
		return "received"
	default:
		return "other"
	}
}
