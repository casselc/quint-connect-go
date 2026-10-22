package guarded_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"example.com/syncfixture/guarded"
	"github.com/casselc/quint-connect-go/qenv"
)

// Run woven (qweave go test): the embedded lock, the method value and the
// goroutine are the simulation's, so tasks interleave inside the critical
// sections and none is lost. Unwoven, the second task to take a real lock
// blocks the scheduler and the run hangs.
func TestWovenEscapes(t *testing.T) {
	for seed := uint64(0); seed < 20; seed++ {
		s := qenv.New(qenv.NewSeeded(seed))
		var c guarded.Counter
		st := &guarded.Store{Rows: map[string]int{}}
		var wg sync.WaitGroup
		read := 0
		for i := range 4 {
			s.Spawn("worker", func(ctx context.Context) {
				c.Inc(ctx)
				st.Put(ctx, "k", i)
				unlock := st.RLock()
				read += st.Rows["k"] * 0
				unlock()
				// its own task: Background resolves to it (not the worker's ctx)
				guarded.Spawn(&wg, func() { c.Inc(context.Background()) })
			})
		}
		s.Spawn("waiter", func(ctx context.Context) {
			qenv.Sleep(ctx, time.Second)
			wg.Wait()
		})
		if err := s.Run(time.Minute); err != nil {
			t.Fatal(err)
		}
		if c.N != 8 {
			t.Fatalf("seed %d: counter %d, want 8 (an increment was lost: the embedded lock did not exclude)", seed, c.N)
		}
		s.Close()
	}
}
