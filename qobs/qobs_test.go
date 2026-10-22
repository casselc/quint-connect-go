package qobs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A file sink is durable: its steps are in the file as soon as they are recorded.
func TestFileSinkIsDurable(t *testing.T) {
	p := filepath.Join(t.TempDir(), "steps.jsonl")
	sink, err := FileSink(p)
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

// Steps recorded elsewhere go to the sink unchanged.
func TestRecorderAdd(t *testing.T) {
	mem := &MemorySink{}
	elsewhere := &MemorySink{}
	NewRecorder(elsewhere, WithID("n1")).Record(context.Background(), "deposit", nil, "amount", 1)
	NewRecorder(mem, WithID("host")).Add(elsewhere.Steps()...)
	got := mem.Steps()
	if len(got) != 1 || got[0].Recorder != "n1" || got[0].Action != "deposit" || got[0].Seq != 1 {
		t.Fatalf("got %+v", got)
	}
}

// A second global install fails while one is in place, and a restore
// puts back what was there before only if its recorder is still the
// installed one.
func TestInstallGlobal(t *testing.T) {
	var a, b MemorySink
	ra, rb := NewRecorder(&a), NewRecorder(&b)
	restore, err := InstallGlobal(ra)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InstallGlobal(rb); err == nil || !strings.Contains(err.Error(), "must not run in parallel") {
		t.Errorf("second install: %v, want it refused", err)
	}
	Record(context.Background(), "a", nil)
	restore()
	restore() // idempotent
	if Global() != nil {
		t.Errorf("restore left %p installed", Global())
	}
	restore, err = InstallGlobal(rb)
	if err != nil {
		t.Fatalf("install after restore: %v", err)
	}
	SetGlobal(ra) // something else replaced it
	restore()
	if Global() != ra {
		t.Error("restore removed a recorder it had not installed")
	}
	SetGlobal(nil)
	if len(a.Steps()) != 1 || len(b.Steps()) != 0 {
		t.Errorf("steps a %v, b %v", a.Steps(), b.Steps())
	}
}
