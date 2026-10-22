package twophase_test

import (
	"context"
	"testing"

	"github.com/casselc/quint-connect-go/qobs"
	"github.com/casselc/quint-connect-go/qtest"
)

const binding = "twophase.binding.yaml"

// steps records a run of named transitions, as the implementation would.
func steps(names ...[2]string) *qobs.MemorySink {
	mem := &qobs.MemorySink{}
	r := qobs.NewRecorder(mem)
	ctx := qobs.WithRecorder(context.Background(), r)
	for _, n := range names {
		if n[1] == "" {
			r.Record(ctx, n[0], nil)
		} else {
			r.Record(ctx, n[0], nil, "node", n[1])
		}
	}
	return mem
}

// A choreo model is checked like any other: its transitions are named
// actions of the node that takes them.
func TestChoreoBinding(t *testing.T) {
	qtest.RequireQuint(t)
	ok := steps([2]string{"prepare", "p1"}, [2]string{"prepare", "p2"}, [2]string{"prepare", "p3"},
		[2]string{"decideCommit", ""}, [2]string{"commitAsInstructed", "p2"})
	qtest.Validate(t, binding, ok.Steps()).Expect(t, qtest.Conforms())
	early := steps([2]string{"prepare", "p1"}, [2]string{"decideCommit", ""})
	qtest.Validate(t, binding, early.Steps()).Expect(t, qtest.Rejects("decideCommit"))
}
