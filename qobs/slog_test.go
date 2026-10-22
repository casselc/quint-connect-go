package qobs_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/casselc/quint-connect-go/qobs"
)

// Log records with quint.action are steps; their attributes are the
// step's fields (groups joined with "."), "error" its error; every record
// still reaches the log, whatever its level.
func TestSlogHandlerRecordsSteps(t *testing.T) {
	mem := &qobs.MemorySink{}
	ctx := qobs.WithRecorder(context.Background(), qobs.NewRecorder(mem))
	var logged bytes.Buffer
	log := slog.New(qobs.NewSlogHandler(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelWarn})))
	log.InfoContext(ctx, "deposited", qobs.ActionKey, "deposit", "account", "alice", "amount", 5, slog.Group("obs", "balance", 15))
	log.With("account", "bob").ErrorContext(ctx, "refused", qobs.ActionKey, "withdraw", "amount", 50, "error", errors.New("insufficient funds"))
	log.WarnContext(ctx, "slow disk")
	steps := mem.Steps()
	if len(steps) != 2 {
		t.Fatalf("%d steps; want 2: %+v", len(steps), steps)
	}
	d, w := steps[0], steps[1]
	if d.Action != "deposit" || d.Outcome != "ok" || d.Args["account"] != "alice" || d.Args["amount"] != int64(5) || d.Obs["balance"] != int64(15) {
		t.Errorf("deposit step: %+v", d)
	}
	if w.Action != "withdraw" || w.Outcome != "error" || w.Error != "insufficient funds" || w.Args["account"] != "bob" {
		t.Errorf("withdraw step: %+v", w)
	}
	if out := logged.String(); strings.Contains(out, "deposited") || !strings.Contains(out, "refused") || !strings.Contains(out, "slow disk") {
		t.Errorf("the log kept its own level:\n%s", out)
	}
}
