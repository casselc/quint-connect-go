package qtest

import (
	"strings"
	"testing"

	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qtrace"
	"github.com/casselc/quint-connect-go/validate"
)

// One report for an exploration: verdicts by reason, faults reached and
// faulted, pairs of kinds together.
func TestEvidence(t *testing.T) {
	ok := &validate.Report{Actors: []*validate.Result{{Conforms: validate.Pass}}}
	violates := &validate.Report{Actors: []*validate.Result{{Conforms: validate.Pass,
		Invariants: []validate.InvariantResult{{Name: "chargedAtMostOnce", Verdict: validate.Fail}}}}}
	rejects := &validate.Report{Actors: []*validate.Result{{Conforms: validate.Fail,
		Failure: &validate.Failure{Kind: "disabled", Step: &qtrace.Step{Action: "ship"}}}}}
	both := []qenv.Choice{{Kind: "fault.a", N: 2, Value: 1}, {Kind: "fault.b", N: 2, Value: 1}}
	x := &Exploration{Runs: []*Run{
		{Report: ok, Choices: both},
		{Report: violates, Choices: []qenv.Choice{{Kind: "fault.a", N: 2, Value: 1}}},
		{Report: rejects, Choices: []qenv.Choice{{Kind: "fault.c", N: 2}}},
	}}
	s := x.Evidence().String()
	for _, want := range []string{"3 runs", "1 conform · 1 rejects ship · 1 violates chargedAtMostOnce",
		"2 of 3 kinds faulted", "pairs 1/1 together", "never faulted: c"} {
		if !strings.Contains(s, want) {
			t.Errorf("no %q in:\n%s", want, s)
		}
	}
	t.Log(s)
}
