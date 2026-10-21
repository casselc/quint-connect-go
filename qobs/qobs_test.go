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
