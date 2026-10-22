package store

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/casselc/quint-connect-go/qcover"
	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qobs"
)

// Woven with -cover, each step's blocks are known: the fault branch is
// put/error's, the code after the last step is no step's, Compact never
// runs, and get's two outcomes run the same code.
func TestCoverageByStep(t *testing.T) {
	if len(qcover.Blocks()) == 0 {
		t.Fatal("not woven: run with qweave -cover")
	}
	var runs []*qcover.Counts
	for seed := uint64(1); seed <= 10; seed++ {
		code := qcover.NewCounts()
		ch := qenv.NewSeeded(seed)
		ch.Rates = qenv.Rates{Default: 0.5}
		ctx := qobs.WithRecorder(qcover.WithCounts(context.Background(), code), qobs.NewRecorder(code.Sink(&qobs.MemorySink{})))
		sim := qenv.New(qenv.WithContext(ch, ctx))
		st := New()
		sim.Process("app", false, func(ctx context.Context) {
			for _, k := range []string{"a", "b", "absent"} {
				_ = st.Put(ctx, k, "v")
				st.Get(ctx, k)
				qenv.Sleep(ctx, time.Second)
			}
			st.Close()
			Kind(1, nil)
		})
		if err := sim.Run(time.Minute); err != nil {
			t.Fatal(err)
		}
		sim.Close()
		runs = append(runs, code)
	}
	s := qcover.Summarize(runs)
	t.Log(s)
	var never []string
	for _, b := range s.Uncovered() {
		never = append(never, b.Func)
	}
	if !slices.Contains(never, "example.com/coverfixture/store.(Store).Compact") {
		t.Errorf("Compact is covered: never run %v", never)
	}
	failBranch := -1
	for _, b := range s.Blocks {
		if b.Func == "example.com/coverfixture/store.(Store).Put" && b.Line == lineOf(t, `qobs.Record(ctx, "put", err`) {
			failBranch = b.ID
		}
	}
	if got := s.Actions(failBranch); !slices.Equal(got, []string{"put/error"}) {
		t.Errorf("the fault branch ran under %v, want put/error only", got)
	}
	var noStep []string
	for id := range s.OnlyOff {
		if f := s.Blocks[id].Func; !slices.Contains(noStep, f) {
			noStep = append(noStep, f)
		}
	}
	slices.Sort(noStep)
	if want := []string{"example.com/coverfixture/store.(Store).Close", "example.com/coverfixture/store.Kind", "example.com/coverfixture/store.New"}; !slices.Equal(noStep, want) {
		t.Errorf("run by no step: %v, want %v (setup, and code after the last step)", noStep, want)
	}
	// clauses are blocks of their own: Kind(1, nil) ran the int case, not
	// the string case
	ran := map[int]bool{}
	for _, b := range s.Blocks {
		if b.Func == "example.com/coverfixture/store.Kind" && s.Hit[b.ID] > 0 {
			ran[b.Line] = true
		}
	}
	if !ran[lineOf(t, `return "positive"`)-1] || ran[lineOf(t, `return "string"`)] {
		t.Errorf("Kind's blocks run, by line: %v", ran)
	}
	if !slices.Contains(s.Indistinct(), [2]string{"get/error", "get/ok"}) {
		t.Errorf("indistinct: %v", s.Indistinct())
	}
}

// lineOf is the line of store.go holding text.
func lineOf(t *testing.T, text string) int {
	b, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatal(err)
	}
	for i, l := range strings.Split(string(b), "\n") {
		if strings.Contains(l, text) {
			return i + 1
		}
	}
	t.Fatalf("no %q in store.go", text)
	return 0
}
