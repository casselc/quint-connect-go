package bank

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/casselc/quint-connect-go/qobs"
	"github.com/casselc/quint-connect-go/qtest"
)

// A service whose only telemetry is its structured log: the records that
// carry quint.action are model steps (qobs.SlogHandler), checked with the
// bank's binding like any other. A log line reporting a wrong balance is a
// mismatch, as a recorded one would be.
func TestSlogStepsConform(t *testing.T) {
	qtest.RequireQuint(t)
	run := func(final int) *qobs.MemorySink {
		mem := &qobs.MemorySink{}
		ctx := qobs.WithRecorder(context.Background(), qobs.NewRecorder(mem))
		log := slog.New(qobs.NewSlogHandler(slog.DiscardHandler))
		log.InfoContext(ctx, "deposit", qobs.ActionKey, "deposit", "account", "alice", "amount", 5, "obs.balance", 5)
		log.WarnContext(ctx, "refused", qobs.ActionKey, "withdraw", "account", "bob", "amount", 3, "obs.balance", 0, "error", errors.New("insufficient funds"))
		log.InfoContext(ctx, "transfer", qobs.ActionKey, "transfer", "src", "alice", "dst", "bob", "amount", 2,
			slog.Group("obs", "src_balance", 3, "dst_balance", final))
		return mem
	}
	qtest.Validate(t, "bank.binding.yaml", run(2).Steps()).Expect(t, qtest.Conforms())
	qtest.Validate(t, "bank.binding.yaml", run(7).Steps()).Expect(t, qtest.Mismatch("transfer"))
}
