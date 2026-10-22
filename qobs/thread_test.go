package qobs

import (
	"context"
	"strings"
	"testing"
)

// A Thread asserts program order: steps of one goroutine. Recording it from
// a second goroutine is an issue, found as it records.
func TestThreadFromTwoGoroutines(t *testing.T) {
	r := NewRecorder(&MemorySink{})
	ctx := Thread(context.Background())
	r.Record(ctx, "a", nil)
	r.Record(ctx, "b", nil)
	if len(r.Issues()) != 0 {
		t.Fatalf("one goroutine: %v", r.Issues())
	}
	done := make(chan struct{})
	go func() { defer close(done); r.Record(ctx, "c", nil) }()
	<-done
	if is := r.Issues(); len(is) != 1 || !strings.Contains(is[0], `recorded "c" from goroutine`) || !strings.Contains(is[0], "give each its own") {
		t.Errorf("two goroutines: %v", is)
	}
}
