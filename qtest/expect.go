package qtest

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"testing"

	"github.com/casselc/quint-connect-go/validate"
)

// RequireQuint skips the test when the quint CLI is not installed.
func RequireQuint(t testing.TB) {
	t.Helper()
	if _, err := exec.LookPath("quint"); err != nil {
		t.Skip("quint not installed (npm i -g @informalsystems/quint)")
	}
}

// An Expectation is what a test expects of a verdict: it returns nil when
// the report meets it, else why not.
type Expectation struct {
	desc  string
	check func(*validate.Report) bool
	every bool // as a Claim alone: every run (Conforms), not some run
	// the actions and invariants it names, checked against the model: a
	// misspelled name would make None(...) hold vacuously
	actions, invariants []string
}

func (e Expectation) String() string { return e.desc }

// Conforms: every actor's run is a behaviour of the model and satisfies
// its invariants.
func Conforms() Expectation {
	return Expectation{"the run conforms and satisfies every invariant", func(r *validate.Report) bool { return r.OK() }, true, nil, nil}
}

func failed(r *validate.Report, kind, action string) bool {
	for _, a := range r.Actors {
		f := a.Failure
		if f != nil && f.Kind == kind && (action == "" || (f.Step != nil && f.Step.Action == action) || strings.HasPrefix(f.Call, action+"(") || f.Call == action) {
			return true
		}
	}
	return false
}

// Rejects: the model cannot take an observed step of this action (an
// implementation bug, or a wrong binding). "" matches any action.
func Rejects(action string) Expectation {
	return Expectation{desc: fmt.Sprintf("a %s step the model cannot take", orAny(action)), check: func(r *validate.Report) bool { return failed(r, "disabled", action) }, actions: named(action)}
}

// Mismatch: after a step of this action, the observed state differs from
// the model's (the binding's expect fails). "" matches any action.
func Mismatch(action string) Expectation {
	return Expectation{desc: fmt.Sprintf("a state mismatch after a %s step", orAny(action)), check: func(r *validate.Report) bool { return failed(r, "state", action) }, actions: named(action)}
}

// Violates: the run is a behaviour of the model, and the model's invariant
// fails on it: a property the design (or this binding's view of it)
// does not guarantee, seen in a real run.
func Violates(invariant string) Expectation {
	return Expectation{desc: fmt.Sprintf("a conforming run that violates %s", invariant), check: func(r *validate.Report) bool {
		for _, a := range r.Actors {
			if a.Conforms != validate.Pass {
				continue
			}
			for _, inv := range a.Invariants {
				if inv.Name == invariant && inv.Verdict == validate.Fail {
					return true
				}
			}
		}
		return false
	}, invariants: []string{invariant}}
}

// AnyOf: the report meets at least one of es.
func AnyOf(es ...Expectation) Expectation {
	e := Expectation{desc: describeOr(es), check: func(r *validate.Report) bool {
		for _, e := range es {
			if e.check(r) {
				return true
			}
		}
		return false
	}}
	for _, x := range es {
		e.actions = append(e.actions, x.actions...)
		e.invariants = append(e.invariants, x.invariants...)
	}
	return e
}

func named(action string) []string {
	if action == "" {
		return nil
	}
	return []string{action}
}

// unknownNames says which actions and invariants es name that the model
// (actions) or the checked invariants do not have; nil sets skip a check.
func unknownNames(es []Expectation, actions, invariants map[string]bool) []string {
	var out []string
	for _, e := range es {
		for _, a := range e.actions {
			if actions != nil && !actions[a] {
				out = append(out, fmt.Sprintf("action %q (the model's: %s)", a, strings.Join(keysOf(actions), ", ")))
			}
		}
		for _, i := range e.invariants {
			if invariants != nil && !invariants[i] {
				out = append(out, fmt.Sprintf("invariant %q (checked: %s)", i, strings.Join(keysOf(invariants), ", ")))
			}
		}
	}
	return out
}

// reportInvariants are the invariants a report checked (nil: none known).
func reportInvariants(r *validate.Report) map[string]bool {
	var m map[string]bool
	for _, a := range r.Actors {
		for _, inv := range a.Invariants {
			if m == nil {
				m = map[string]bool{}
			}
			m[inv.Name] = true
		}
	}
	return m
}

func describeOr(es []Expectation) string {
	var s []string
	for _, e := range es {
		s = append(s, e.desc)
	}
	return strings.Join(s, ", or ")
}

func orAny(action string) string {
	if action == "" {
		return "model"
	}
	return action
}

// A Claim is what a test expects of a set of runs (Exploration.Expect,
// Sweep.Expect): that some, every or no run meets expectations. An
// Expectation alone is a claim about some run (Some), except Conforms,
// which is about every run (All); several claims are judged one by one,
// so Expect(t, Violates("a"), Violates("b")) wants each shown by some
// run, not one run showing both (Some(a, b) says that).
type Claim interface {
	judge(reports []*validate.Report) judgment
	String() string
}

// judgment is a claim's verdict: show is the run to print (-1: none).
type judgment struct {
	ok   bool
	show int
	n    int // runs meeting (Some, None) or missing (All) the expectations
}

func (e Expectation) judge(rs []*validate.Report) judgment {
	if e.every {
		return All(e).judge(rs)
	}
	return Some(e).judge(rs)
}

type quantified struct {
	kind string // "some", "all", "none"
	es   []Expectation
}

// Some: one run meets every one of es.
func Some(es ...Expectation) Claim { return quantified{"some", es} }

// All: every run meets every one of es.
func All(es ...Expectation) Claim { return quantified{"all", es} }

// None: no run meets any of es: what a bug must not do, or a row where a
// fault cannot happen must never show.
func None(es ...Expectation) Claim { return quantified{"none", es} }

func (q quantified) String() string {
	var d []string
	for _, e := range q.es {
		d = append(d, e.desc)
	}
	switch q.kind {
	case "some":
		return "some run showing " + strings.Join(d, " and ")
	case "all":
		return "every run showing " + strings.Join(d, " and ")
	}
	return "no run showing " + strings.Join(d, ", or ")
}

func (q quantified) meets(r *validate.Report) (all, any bool) {
	all = true
	for _, e := range q.es {
		ok := r != nil && e.check(r)
		all = all && ok
		any = any || ok
	}
	return all, any
}

func (q quantified) judge(rs []*validate.Report) judgment {
	j := judgment{show: -1}
	for i, r := range rs {
		all, any := q.meets(r)
		switch q.kind {
		case "some":
			if all {
				j.n++
				if j.show < 0 {
					j.show = i
				}
			}
		case "all":
			if !all {
				j.n++
				if j.show < 0 {
					j.show = i
				}
			}
		case "none":
			if any {
				j.n++
				if j.show < 0 {
					j.show = i
				}
			}
		}
	}
	j.ok = (q.kind == "some") == (j.n > 0)
	return j
}

// Expect fails t unless the report meets every expectation, printing the
// report. It is how a test states what a mutant must look like:
//
//	run := qtest.Record(t, "bank.binding.yaml", scenarioWith(LostDeposit))
//	qtest.Expect(t, run.Report, qtest.Mismatch("deposit"))
func Expect(t testing.TB, r *validate.Report, es ...Expectation) {
	t.Helper()
	if r == nil {
		t.Fatalf("quintgo: no report")
	}
	if u := unknownNames(es, nil, reportInvariants(r)); len(u) > 0 {
		t.Fatalf("quintgo: the expectation names %s: a misspelled name never matches", strings.Join(u, "; "))
	}
	for _, e := range es {
		if !e.check(r) {
			t.Fatalf("quintgo: want %s, got:\n%s", e, r.Summary())
		}
	}
}

// Expect is qtest.Expect on the run's report.
func (r *Run) Expect(t testing.TB, es ...Expectation) {
	t.Helper()
	Expect(t, r.Report, es...)
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
