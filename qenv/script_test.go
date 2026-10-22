package qenv_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/casselc/quint-connect-go/qenv"
)

// A script makes its faults where it says and no others: the third write
// fails, then the writer's process crashes after the fourth step; the
// crashable process's random crashes and the reader's fault never happen.
func TestScript(t *testing.T) {
	var log []string
	sc := &qenv.Script{
		Faults: []qenv.Scripted{
			{Kind: "test.write", Option: 1, After: 2},
			{Kind: "nemesis.crash.writer", After: 4},
		},
		Progress: func() int { return len(log) },
	}
	s := qenv.New(sc)
	var p *qenv.Process
	p = s.Process("writer", true, func(ctx context.Context) {
		if p != nil && p.Incarnation > 1 {
			log = append(log, "restarted")
			return
		}
		for range 6 {
			if err := qenv.FaultPoint(ctx, "test.write", false, errUnavailable); errors.Is(err, errUnavailable) {
				log = append(log, "fail")
			} else {
				log = append(log, "write")
			}
			if qenv.FaultPoint(ctx, "test.read", false) != nil {
				log = append(log, "read failed")
			}
			qenv.Sleep(ctx, time.Second)
		}
	})
	if err := s.Run(time.Minute); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if want := []string{"write", "write", "fail", "write", "restarted"}; !reflect.DeepEqual(log, want) {
		t.Errorf("log %v, want %v", log, want)
	}
	if r := sc.Remaining(); len(r) != 0 {
		t.Errorf("remaining %v", r)
	}
}
