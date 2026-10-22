package qrun_test

import (
	"context"
	"testing"
	"time"

	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qrun"
)

var name = qrun.NewKey[string]("name")

// A value is found in the context, else in the goroutine's run: a task's,
// or the run of the goroutine that created the simulation. Outside any
// run, nowhere.
func TestGet(t *testing.T) {
	if _, ok := qrun.Get(context.Background(), name); ok {
		t.Fatal("found outside a run")
	}
	s := qenv.New(qenv.WithContext(qenv.Nominal{}, qrun.With(context.Background(), name, "run")))
	if v, _ := qrun.Get(nil, name); v != "run" {
		t.Errorf("the creator's run: %q", v)
	}
	s.Process("p", false, func(ctx context.Context) {
		if v, _ := qrun.Get(context.TODO(), name); v != "run" {
			t.Errorf("a task's run: %q", v)
		}
		if v, _ := qrun.Get(qrun.With(context.TODO(), name, "mine"), name); v != "mine" {
			t.Errorf("the context first: %q", v)
		}
		if task, ok := qrun.In(ctx, qrun.Task); !ok || task == nil {
			t.Error("a task's context names its task")
		}
	})
	if err := s.Run(time.Second); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, ok := qrun.Get(nil, name); ok {
		t.Error("found after the run")
	}
}
