package qtest

import (
	"strings"
	"testing"

	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/validate"
)

// The messages for the traps of explorations: every run failing at its
// first step names the constants, and faults a script never made are
// listed.
func TestTrapMessages(t *testing.T) {
	failAt := func(i int) *validate.Report {
		return &validate.Report{Actors: []*validate.Result{{Conforms: validate.Fail, Failure: &validate.Failure{Index: i}}}}
	}
	x := &Exploration{constants: map[string]string{"STOCK": "2"}, Runs: []*Explored{{Report: failAt(0)}, {Report: failAt(0)}}}
	if m := x.atStart(); !strings.Contains(m, "all 2 runs fail at step 1") || !strings.Contains(m, "STOCK=2") {
		t.Errorf("at start: %q", m)
	}
	x.Runs[1].Report = failAt(3)
	if m := x.atStart(); m != "" {
		t.Errorf("a run failing later: %q", m)
	}
	x.Runs[0].Unscripted = []qenv.Scripted{{Kind: "rpc.api.request", Option: 1, After: 14}}
	if m := x.unscripted(); !strings.Contains(m, "in 1 of 2 runs scripted faults never happened") || !strings.Contains(m, "rpc.api.request=1 after 14 steps") {
		t.Errorf("unscripted: %q", m)
	}
}
