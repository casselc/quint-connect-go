// Package qmut runs mutants of the code under test, woven by qweave
// (qweave -mutate): every mutant of a package is compiled into one build,
// each behind a switch, and a run activates one mutant through its
// context (WithMutant), found like a run's recorder (qrun). Runs of
// different mutants then share a process, and a simulation's seeds of one
// mutant run side by side (mutant schemata).
//
// A mutant that every exploration survives is code the model does not
// constrain: it can change and no check notices (qtest.System.Mutate).
package qmut

import (
	"context"
	"fmt"
	"sync"

	"github.com/casselc/quint-connect-go/qrun"
)

// A Mutant is one woven change.
type Mutant struct {
	ID   int
	File string
	Line int
	Func string
	// Op is the change: "negate if", "<= for <" and the like, "drop call".
	Op string
	// Code is the original code, for reports.
	Code string
}

func (m Mutant) String() string {
	return fmt.Sprintf("%s:%d %s: %s (%s)", m.File, m.Line, m.Func, m.Op, m.Code)
}

var (
	mu      sync.Mutex
	mutants []Mutant
)

// Register adds a file's mutants and returns the ID of the first: woven
// code numbers them from it.
func Register(ms []Mutant) int {
	mu.Lock()
	defer mu.Unlock()
	base := len(mutants)
	for i, m := range ms {
		m.ID = base + i
		mutants = append(mutants, m)
	}
	return base
}

// Mutants are all registered mutants, by ID.
func Mutants() []Mutant {
	mu.Lock()
	defer mu.Unlock()
	return append([]Mutant(nil), mutants...)
}

var active = qrun.NewKey[int]("mutant")

// WithMutant makes ctx's run run mutant id (its simulation's tasks, whose
// contexts derive from ctx).
func WithMutant(ctx context.Context, id int) context.Context { return qrun.With(ctx, active, id+1) }

// A Reach is the mutants whose code a run reached, on or off.
type Reach struct {
	mu  sync.Mutex
	ids map[int]bool
}

// Reached is whether mutant id's code ran.
func (r *Reach) Reached(id int) bool { r.mu.Lock(); defer r.mu.Unlock(); return r.ids[id] }

var reach = qrun.NewKey[*Reach]("mutant reach")

// WithReach makes ctx's run note the mutants whose code it reaches.
func WithReach(ctx context.Context, r *Reach) context.Context { return qrun.With(ctx, reach, r) }

// On reports whether mutant id is the calling code's run's mutant, and
// notes that its code was reached.
func On(id int) bool {
	rc, _ := qrun.Current()
	if r, ok := qrun.In(rc, reach); ok && r != nil {
		r.mu.Lock()
		if r.ids == nil {
			r.ids = map[int]bool{}
		}
		r.ids[id] = true
		r.mu.Unlock()
	}
	v, _ := qrun.In(rc, active)
	return v == id+1
}

// Cond is an if's condition c, negated when mutant id is on.
func Cond(id int, c bool) bool {
	if On(id) {
		return !c
	}
	return c
}

// Pick is orig, or mutated when mutant id is on: a comparison with its
// boundary moved.
func Pick(id int, orig, mutated bool) bool {
	if On(id) {
		return mutated
	}
	return orig
}
