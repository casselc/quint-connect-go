// Package store has fault points and no fault-injection code of its own.
package store

import (
	"context"
	"errors"
)

// ErrUnavailable is the store's own error, injected at its fault point.
var ErrUnavailable = errors.New("store unavailable")

// Store is a map.
type Store struct{ m map[string]string }

// New returns an empty store.
func New() *Store { return &Store{m: map[string]string{}} }

// Get reads k. Its results are unnamed: weaving names them.
//
//quint:fault kind:store.get error:ErrUnavailable
func (s *Store) Get(ctx context.Context, k string) (string, error) {
	return s.m[k], nil
}

// Put writes k, or its process crashes first.
//
//quint:fault kind:store.put crash:true
func (s *Store) Put(ctx context.Context, k, v string) error {
	s.m[k] = v
	return nil
}

// Flush is never called: its point is declared and never reached.
//
//quint:fault kind:store.flush
func (s *Store) Flush(ctx context.Context) error { return nil }
