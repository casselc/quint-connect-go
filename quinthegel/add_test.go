package quinthegel_test

import (
	"context"
	"testing"

	"github.com/casselc/quint-connect-go/qobs"
	"github.com/casselc/quint-connect-go/quinthegel"
	"hegel.dev/go/hegel"
)

// Steps recorded elsewhere (in gosim, another process) join a test case's
// own steps unchanged: their recorder, sequence number and order keys.
func TestRecorderAdd(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		rec := quinthegel.NewRecorder(ht)
		rec.Step("deposit", nil, "acct", "a", "amount", 1)
		mem := &qobs.MemorySink{}
		qobs.NewRecorder(mem, qobs.WithID("n1")).Record(context.Background(), "withdraw", nil, "acct", "a", "amount", 1, "order.lamport", 7)
		rec.Add(mem.Steps()...)
		got := rec.Steps()
		if len(got) != 2 || got[1].Action != "withdraw" || got[1].Recorder != "n1" || got[1].Order["lamport"] != 7 {
			ht.Fatalf("got %+v", got)
		}
	}, hegel.WithTestCases(1))
}
