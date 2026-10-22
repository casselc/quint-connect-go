package checkout_test

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/casselc/quint-connect-go/examples/checkout"
	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qobs"
	"github.com/casselc/quint-connect-go/qtest"
)

// The shop on qenv's native runtime inside a testing/synctest bubble:
// real goroutines and Go's own scheduler, with the bubble's fake clock, so
// 80 s of timeouts, retries and redeliveries take milliseconds. The
// schedule is Go's, not a chooser's, so a run is not reproducible from its
// seed; the faults still are the chooser's, and the model judges the run.
func TestNativeInSynctest(t *testing.T) {
	qtest.RequireQuint(t)
	mem := &qobs.MemorySink{}
	var took time.Duration
	synctest.Test(t, func(t *testing.T) {
		c := qenv.NewSeeded(3)
		c.Rates = crashy
		ctx := qobs.WithRecorder(context.Background(), qobs.NewRecorder(mem))
		start := time.Now() //qenv:ok the synctest bubble's clock, which the native runtime's sleeps advance
		if _, err := checkout.RunOn(qenv.NewNative(qenv.WithContext(c, ctx), nil), checkout.Config{Isolation: qenv.Serializable}); err != nil {
			t.Fatal(err)
		}
		took = time.Since(start) //qenv:ok the bubble's clock
	})
	if took < 20*time.Second {
		t.Errorf("the bubble's clock moved %v; want the run's 20 s and more", took)
	}
	t.Logf("%d steps in %v of the bubble's time", len(mem.Steps()), took)
	qtest.Validate(t, binding, mem.Steps()).Expect(t, qtest.Conforms())
}
