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
			{Kind: "test.scripted", Option: 1, After: 2},
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
			if err := qenv.FaultPoint(ctx, "test.scripted", false, errUnavailable); errors.Is(err, errUnavailable) {
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
	sc := &qenv.Script{Faults: []qenv.Scripted{{Kind: "test.scripted", Option: 1, After: 2}}}
	rec := &qenv.ChoiceLog{C: sc}
	s := qenv.New(qenv.WithContext(rec, qobs.WithRecorder(context.Background(), qobs.NewRecorder(mem))))
	s.Process("writer", false, func(ctx context.Context) {
		for range 4 {
			qobs.Record(ctx, "write", qenv.FaultPoint(ctx, "test.scripted", false, errUnavailable))
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
		if c.Kind == "fault.test.scripted" {
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
		rec := &qenv.ChoiceLog{C: c}
		s := qenv.New(rec)
		var log []string
		for _, name := range []string{"a", "b", "c"} {
			s.Process(name, true, func(ctx context.Context) {
				for i := range 5 {
					if qenv.FaultPoint(ctx, "test.scripted", false, errUnavailable) != nil {
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

// Options by name: a script naming lostResponse waits through the reads,
// which cannot lose a response, and fires at the first update; coverage
// names the options; a restriction that is not a prefix is refused.
func TestNamedFaultOptions(t *testing.T) {
	kv := qenv.DeclareFaults("test.kv", "unavailable", "lostResponse")
	sc := &qenv.Script{Faults: []qenv.Scripted{{Kind: "test.kv", Name: "lostResponse"}}}
	rec := &qenv.ChoiceLog{C: sc}
	s := qenv.New(rec)
	var got []string
	s.Process("client", false, func(ctx context.Context) {
		for _, op := range []string{"get", "get", "update", "update"} {
			k := kv
			if op == "get" {
				k = kv.Only("unavailable")
			}
			got = append(got, op+":"+s.FaultOf(k))
			qenv.Sleep(ctx, time.Millisecond)
		}
	})
	if err := s.Run(time.Second); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if want := []string{"get:", "get:", "update:lostResponse", "update:"}; !reflect.DeepEqual(got, want) {
		t.Errorf("faults %v, want %v", got, want)
	}
	cov := qenv.CoverageOf([][]qenv.Choice{rec.Choices})
	if p := cov.Point("test.kv"); p == nil || !reflect.DeepEqual(p.Names, []string{"unavailable", "lostResponse"}) || !strings.Contains(cov.String(), "lostResponse 1") {
		t.Errorf("coverage:\n%s", cov)
	}
	defer func() {
		if recover() == nil {
			t.Error("Only(lostResponse), not a prefix, was accepted")
		}
	}()
	kv.Only("lostResponse")
}

// Rates are set by kind as reports print it, without touching the rates
// they start from; NoFaults leaves every fault nominal.
func TestRatesSet(t *testing.T) {
	r := qenv.DefaultRates.Set("rpc.payments.response", 0.3)
	if r.Of("fault.rpc.payments.response") != 0.3 || r.Of("fault.rpc.payments.request") != qenv.DefaultRates.Default {
		t.Errorf("set: %+v", r)
	}
	if qenv.DefaultRates.Of("fault.rpc.payments.response") != qenv.DefaultRates.Default {
		t.Error("Set changed DefaultRates")
	}
	if qenv.NoFaults.Of("fault.nemesis") != 0 || qenv.NoFaults.Of("fault.store.get") != 0 {
		t.Errorf("NoFaults: %+v", qenv.NoFaults)
	}
}

// Faults of different kinds are independent: a fault whose phase has
// passed (the first kind never comes) no longer blocks the next, unless
// the next follows it.
func TestScriptPartialOrder(t *testing.T) {
	run := func(sc *qenv.Script) []string {
		var got []string
		s := qenv.New(sc)
		defer s.Close()
		s.Spawn("w", func(ctx context.Context) {
			for range 3 {
				if qenv.FaultPoint(ctx, "test.second", false) != nil {
					got = append(got, "second")
				}
				qenv.Sleep(ctx, time.Second)
			}
		})
		if err := s.Run(time.Minute); err != nil {
			t.Fatal(err)
		}
		return got
	}
	never := qenv.Scripted{Kind: "test.never", Option: 1}
	sc := &qenv.Script{Faults: []qenv.Scripted{never, {Kind: "test.second", Option: 1}}}
	if got := run(sc); len(got) != 1 || len(sc.Remaining()) != 1 {
		t.Errorf("independent: got %v, remaining %v", got, sc.Remaining())
	}
	sc = &qenv.Script{Faults: []qenv.Scripted{never, {Kind: "test.second", Option: 1, Follows: []int{0}}}}
	if got := run(sc); len(got) != 0 || len(sc.Remaining()) != 2 {
		t.Errorf("following: got %v, remaining %v", got, sc.Remaining())
	}
}

// A scripted kind written with the chooser's "fault." prefix fails with the
// fix: it once never matched, and the fault silently never happened.
func TestScriptPrefixedKind(t *testing.T) {
	sc := &qenv.Script{Faults: []qenv.Scripted{{Kind: "fault.test.prefixed", Option: 1}}}
	s := qenv.New(sc)
	s.Spawn("writer", func(ctx context.Context) { _ = qenv.FaultPoint(ctx, "test.prefixed", false, errUnavailable) })
	defer func() {
		r := recover()
		s.Close()
		if msg := fmt.Sprint(r); !strings.Contains(msg, `name it without the "fault." prefix ("test.prefixed")`) {
			t.Errorf("panic %q, want the kind without its prefix", msg)
		}
	}()
	_ = s.Run(time.Minute)
	t.Error("a prefixed kind ran")
}
