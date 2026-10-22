package qcover_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/casselc/quint-connect-go/qcover"
	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qobs"
)

// Two tasks interleave, each running its own blocks between its steps:
// every step gets its own task's blocks, not whatever ran meanwhile, and
// runs in parallel keep their counts apart.
func TestStepsGetTheirTasksBlocks(t *testing.T) {
	base := qcover.Register([]qcover.Block{
		{File: "a.go", Line: 1, Func: "a", Stmts: 1},
		{File: "b.go", Line: 1, Func: "b", Stmts: 1},
		{File: "c.go", Line: 1, Func: "c", Stmts: 1},
	})
	a, b, never := base, base+1, base+2
	run := func() *qcover.Counts {
		code := qcover.NewCounts()
		ctx := qobs.WithRecorder(qcover.WithCounts(context.Background(), code), qobs.NewRecorder(code.Sink(nil)))
		s := qenv.New(qenv.WithContext(qenv.NewSeeded(1), ctx))
		for _, w := range []struct {
			name  string
			block int
		}{{"a", a}, {"b", b}} {
			s.Process(w.name, false, func(ctx context.Context) {
				for range 3 {
					qcover.Hit(w.block)
					qenv.Sleep(ctx, time.Millisecond) // the other task runs here
					qobs.Record(ctx, w.name, nil)
				}
			})
		}
		if err := s.Run(time.Second); err != nil {
			t.Error(err)
		}
		s.Close()
		return code
	}
	runs := make([]*qcover.Counts, 4)
	done := make(chan int)
	for i := range runs {
		go func() { runs[i] = run(); done <- i }() //qenv:ok runs side by side
	}
	for range runs {
		<-done
	}
	for _, c := range runs {
		if h := c.Hits(); h[a] != 3 || h[b] != 3 {
			t.Fatalf("hits %v", h)
		}
		for _, st := range c.Steps() {
			want := map[string]int{"a": a, "b": b}[st.Action]
			if !slices.Equal(st.Blocks, []int{want}) {
				t.Errorf("step %s ran %v, want [%d]", st.Action, st.Blocks, want)
			}
		}
	}
	s := qcover.Summarize(runs)
	if u := s.Uncovered(); !slices.ContainsFunc(u, func(bl qcover.Block) bool { return bl.ID == never }) {
		t.Errorf("uncovered %v", u)
	}
	if got := s.Actions(a); !slices.Equal(got, []string{"a/ok"}) {
		t.Errorf("block a ran under %v", got)
	}
}
