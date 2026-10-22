package qenv_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/casselc/quint-connect-go/qenv"
	"github.com/casselc/quint-connect-go/qobs"
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

// Choices carry the run's step count, and a Script with no Progress waits
// on the run's own recorder: its fault lands after the steps before it.
func TestChoicesAlignToSteps(t *testing.T) {
	mem := &qobs.MemorySink{}
	sc := &qenv.Script{Faults: []qenv.Scripted{{Kind: "test.write", Option: 1, After: 2}}}
	rec := &qenv.Recorder{C: sc}
	s := qenv.New(qenv.WithContext(rec, qobs.WithRecorder(context.Background(), qobs.NewRecorder(mem))))
	s.Process("writer", false, func(ctx context.Context) {
		for range 4 {
			qobs.Record(ctx, "write", qenv.FaultPoint(ctx, "test.write", false, errUnavailable))
		}
	})
	if err := s.Run(time.Minute); err != nil {
		t.Fatal(err)
	}
	s.Close()
	var outcomes []string
	for _, st := range mem.Steps() {
		outcomes = append(outcomes, st.Outcome)
	}
	if want := []string{"ok", "ok", "error", "ok"}; !reflect.DeepEqual(outcomes, want) {
		t.Errorf("outcomes %v, want %v", outcomes, want)
	}
	var at []int
	for _, c := range rec.Choices {
		if c.Kind == "fault.test.write" {
			at = append(at, c.Step)
		}
	}
	if want := []int{0, 1, 2, 3}; !reflect.DeepEqual(at, want) {
		t.Errorf("the write choices came after %v steps, want %v", at, want)
	}
}

// A recorded run's choices encode to bytes from which the byte rule makes
// the same choices: a run becomes a fuzz corpus entry.
func TestEncodeChoices(t *testing.T) {
	rates := qenv.Rates{Default: 0.3}
	run := func(c qenv.Chooser) ([]qenv.Choice, []string) {
		rec := &qenv.Recorder{C: c}
		s := qenv.New(rec)
		var log []string
		for _, name := range []string{"a", "b", "c"} {
			s.Process(name, true, func(ctx context.Context) {
				for i := range 5 {
					if qenv.FaultPoint(ctx, "test.write", false, errUnavailable) != nil {
						log = append(log, fmt.Sprintf("%s%d!", name, i))
					} else {
						log = append(log, fmt.Sprintf("%s%d", name, i))
					}
					qenv.Sleep(ctx, time.Duration(s.Choose("sched.wait", 300))*time.Millisecond)
				}
			})
		}
		if err := s.Run(time.Minute); err != nil {
			t.Fatal(err)
		}
		s.Close()
		return rec.Choices, log
	}
	seeded := qenv.NewSeeded(7)
	seeded.Rates = rates
	choices, want := run(seeded)
	data, err := qenv.EncodeChoices(choices, rates)
	if err != nil {
		t.Fatal(err)
	}
	if _, got := run(qenv.FromBytes(data, rates)); !reflect.DeepEqual(got, want) {
		t.Errorf("replayed from bytes:\n%v\nwant\n%v", got, want)
	}
	// past the end, every choice is nominal: no faults
	if _, got := run(qenv.FromBytes(nil, rates)); strings.Contains(strings.Join(got, " "), "!") {
		t.Errorf("faults from no bytes: %v", got)
	}
}
