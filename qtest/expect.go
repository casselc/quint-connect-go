package qtest

import (
	"fmt"
	"os/exec"
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
}

func (e Expectation) String() string { return e.desc }

// Conforms: every actor's run is a behaviour of the model and satisfies
// its invariants.
func Conforms() Expectation {
	return Expectation{"the run conforms and satisfies every invariant", func(r *validate.Report) bool { return r.OK() }}
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
	return Expectation{fmt.Sprintf("a %s step the model cannot take", orAny(action)), func(r *validate.Report) bool { return failed(r, "disabled", action) }}
}

// Mismatch: after a step of this action, the observed state differs from
// the model's (the binding's expect fails). "" matches any action.
func Mismatch(action string) Expectation {
	return Expectation{fmt.Sprintf("a state mismatch after a %s step", orAny(action)), func(r *validate.Report) bool { return failed(r, "state", action) }}
}

// Violates: the run is a behaviour of the model, and the model's invariant
// fails on it: a property the design (or this binding's view of it)
// does not guarantee, seen in a real run.
func Violates(invariant string) Expectation {
	return Expectation{fmt.Sprintf("a conforming run that violates %s", invariant), func(r *validate.Report) bool {
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
	}}
}

// AnyOf: the report meets at least one of es.
func AnyOf(es ...Expectation) Expectation {
	return Expectation{describeOr(es), func(r *validate.Report) bool {
		for _, e := range es {
			if e.check(r) {
				return true
			}
		}
		return false
	}}
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
