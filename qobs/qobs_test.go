package qobs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A durable sink's steps are in the file as soon as they are recorded.
func TestDurableFileSink(t *testing.T) {
	p := filepath.Join(t.TempDir(), "steps.jsonl")
	sink, err := DurableFileSink(p)
	if err != nil {
		t.Fatal(err)
	}
	NewRecorder(sink).Record(context.Background(), "deposit", nil, "amount", 5)
	b, _ := os.ReadFile(p) // before Close
	if !strings.Contains(string(b), `"quint.action":"deposit"`) {
		t.Fatalf("not written through: %q", b)
	}
	sink.Close()
}

// A context's recorder takes precedence over the global one, so parallel
// runs record separately.
func TestWithRecorder(t *testing.T) {
	var global, local MemorySink
	SetGlobal(NewRecorder(&global))
	defer SetGlobal(nil)
	ctx := WithRecorder(context.Background(), NewRecorder(&local))
	Record(ctx, "a", nil)
	Record(context.Background(), "b", nil)
	if l, g := local.Steps(), global.Steps(); len(l) != 1 || l[0].Action != "a" || len(g) != 1 || g[0].Action != "b" {
		t.Fatalf("local %v, global %v", l, g)
	}
}
