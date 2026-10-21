package validate

import (
	"context"
	"fmt"
	"sort"

	"github.com/casselc/quint-connect-go/binding"
	"github.com/casselc/quint-connect-go/qtrace"
)

// Checker validates steps against one binding. Create it once (it typechecks
// the model) and call Check per run: this is the entry point for Go tests.
type Checker struct {
	Binding *binding.Binding
	Model   *binding.Model
	Options Options
}

// NewChecker loads a binding file, typechecks its model and lints the
// binding against it.
func NewChecker(bindingPath string, opt Options) (*Checker, error) {
	b, err := binding.Load(bindingPath)
	if err != nil {
		return nil, err
	}
	m, err := binding.LoadModel(opt.Quint, b.SpecPath(), b.Module)
	if err != nil {
		return nil, err
	}
	if errs := b.Lint(m); len(errs) > 0 {
		return nil, fmt.Errorf("binding %s does not fit %s: %v", bindingPath, b.Module, errs)
	}
	return &Checker{Binding: b, Model: m, Options: opt}, nil
}

// WithConstants returns a checker for another instance of the same model:
// the binding's constants with these overridden (Quint expressions, or
// templates as in the binding). One binding then serves runs of different
// sizes, such as a load run's stock and a small exploration's.
func (c *Checker) WithConstants(constants map[string]string) (*Checker, error) {
	b := *c.Binding
	b.Constants = map[string]string{}
	for k, v := range c.Binding.Constants {
		b.Constants[k] = v
	}
	for k, v := range constants {
		if _, ok := c.Model.Consts[k]; !ok {
			return nil, fmt.Errorf("constant %s: not a const of %s (it has %v)", k, c.Model.Module, sortedConsts(c.Model.Consts))
		}
		b.Constants[k] = v
	}
	cc := *c
	cc.Binding = &b
	return &cc, nil
}

func sortedConsts(m map[string]string) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// Report is the result of Check: global reconstruction issues and one
// Result per actor.
type Report struct {
	Issues []qtrace.Issue
	Actors []*Result
}

// OK is true when every actor's trace conforms and satisfies the invariants.
func (r *Report) OK() bool {
	for _, a := range r.Actors {
		if !a.OK() {
			return false
		}
	}
	return len(r.Actors) > 0
}

func (r *Report) Summary() string {
	s := ""
	for _, i := range r.Issues {
		s += fmt.Sprintf("issue: %s\n", i)
	}
	for _, a := range r.Actors {
		s += a.Summary()
	}
	return s
}

// Check reconstructs per-actor traces from steps (in any order, from any
// number of recorders) and validates each.
func (c *Checker) Check(ctx context.Context, steps []qtrace.Step) (*Report, error) {
	traces, issues := qtrace.Reconstruct(steps)
	rep := &Report{Issues: issues}
	for _, t := range traces {
		r, err := Validate(ctx, c.Binding, c.Model, t, c.Options)
		if err != nil {
			return rep, fmt.Errorf("actor %s: %w", t.Actor, err)
		}
		rep.Actors = append(rep.Actors, r)
	}
	return rep, nil
}

// WithInvariants returns a copy of the checker that checks these model
// invariants instead of the binding's, e.g. a property the design does not
// guarantee, to see whether (and where) a recorded run violates it.
func (c *Checker) WithInvariants(invariants ...string) (*Checker, error) {
	for _, inv := range invariants {
		if !c.Model.Vals[inv] {
			return nil, fmt.Errorf("invariant %s: not a boolean val of %s", inv, c.Binding.Module)
		}
	}
	b := *c.Binding
	b.Invariants = invariants
	cc := *c
	cc.Binding = &b
	return &cc, nil
}
