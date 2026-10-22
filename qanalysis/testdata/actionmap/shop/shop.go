// Package shop has actions recorded both ways: an annotation, and
// qobs.Record in a closure (as checkout records at commit).
package shop

import (
	"context"

	"github.com/casselc/quint-connect-go/qobs"
)

// Store is where a step takes effect.
type Store interface{ OnCommit(f func()) }

// Shop places and cancels orders.
type Shop struct {
	St    Store
	stock int
}

// Place reserves stock and logs.
//
//quint:action action:place order:id
func (s *Shop) Place(ctx context.Context, id int) error {
	s.reserve(id)
	s.log()
	return nil
}

func (s *Shop) reserve(id int) {
	if id < 0 {
		s.validate(id)
	}
	s.stock--
}

func (s *Shop) validate(id int) {}

func (s *Shop) log() {}

// Cancel releases stock and records at commit.
func (s *Shop) Cancel(ctx context.Context, id int) {
	s.St.OnCommit(func() {
		s.release(id)
		qobs.Record(ctx, "cancel", nil, "order", id)
	})
}

func (s *Shop) release(id int) { s.stock++ }

// Helper is reached from no site.
func Helper() {}
