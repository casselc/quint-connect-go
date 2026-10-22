package qenv

import (
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/casselc/quint-connect-go/qenv/internal/choice"
	"github.com/casselc/quint-connect-go/qobs"
)

// A Scripted fault is one fault a Script makes happen: at the first choice
// of Kind ("fault.<kind>", or "nemesis.<name>" for a nemesis such as
// "nemesis.crash.payments") once the run has taken After steps, the
// script's earlier faults of the same kind have happened, and so have the
// ones it Follows.
type Scripted struct {
	Kind string
	// Option is the fault choice's option; Name, if set, names it instead
	// (a declared kind's, Faults). Ignored for a nemesis.
	Option int
	Name   string
	After  int
	// Follows are the indices (in Script.Faults) of faults of other kinds
	// that must happen first: "a, then b" when the order matters. Faults
	// of different kinds are otherwise independent, so one whose phase has
	// passed blocks no other.
	Follows []int
}

func (s Scripted) String() string {
	if s.Name != "" {
		return fmt.Sprintf("%s=%s after %d steps", s.Kind, s.Name, s.After)
	}
	return fmt.Sprintf("%s=%d after %d steps", s.Kind, s.Option, s.After)
}

// Script is a Chooser that makes exactly the faults it is given, in their
// partial order (each kind's in order, others as Follows says), and no
// others: a model trace's crashes and failures imposed on the program
// (connect.FaultScript), while the program does the rest. Every
// other fault choice is nominal, and non-fault choices are Base's (nil:
// nominal, the canonical schedule).
//
// Progress tells how many steps the run has taken (nil: its step
// recorder's Count, found through the run): a scripted fault waits for the
// steps before it in the trace, so it lands where the trace put it. A fault the run never comes
// to stays in Remaining, and the run did not follow the trace.
type Script struct {
	Faults   []Scripted
	Base     Chooser
	Progress func() int

	mu   sync.Mutex
	done []bool
}

// due returns the fault of kind that may happen now, or -1: the first of
// its kind not done, if what it follows is done and its steps are taken.
func (s *Script) due(kind string) int {
	if s.done == nil {
		s.done = make([]bool, len(s.Faults))
	}
	for i, f := range s.Faults {
		if strings.TrimPrefix(f.Kind, "fault.") != kind || s.done[i] {
			continue
		}
		for _, j := range f.Follows {
			if j < 0 || j >= len(s.Faults) || j == i {
				panic(fmt.Sprintf("qenv: script fault %d (%v) follows %d, which is not another fault of the script", i, f, j))
			}
			if !s.done[j] {
				return -1
			}
		}
		if f.After > 0 && s.progress() < f.After {
			return -1
		}
		return i
	}
	return -1
}

func (s *Script) progress() int {
	if s.Progress != nil {
		return s.Progress()
	}
	if r := qobs.From(nil); r != nil {
		return r.Count()
	}
	return 1 << 30 // no recorder to wait for
}

// Choose implements Chooser.
func (s *Script) Choose(kind string, n int) int {
	if !IsFault(kind) {
		if s.Base == nil {
			return 0
		}
		return s.Base.Choose(kind, n)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := strings.TrimPrefix(kind, "fault.")
	i := s.due(k)
	if i < 0 {
		return 0
	}
	f := s.Faults[i]
	v := f.Option
	if f.Name != "" {
		v = slices.Index(choice.Options(k), f.Name) + 1
		if v == 0 {
			panic(fmt.Sprintf("qenv: script fault %v: %s has no option %q (its options: %v)", f, kind, f.Name, choice.Options(k)))
		}
	}
	if v <= 0 {
		panic(fmt.Sprintf("qenv: script fault %v: option %d is nominal; script the fault's option, from 1", f, v))
	}
	if v >= n {
		return 0 // this choice does not offer the option (a read of a kind whose write can lose its response): wait for one that does
	}
	s.done[i] = true
	return v
}

// ChooseNemesis picks a nemesis by name among the enabled ones: the
// scripted one if it is due, else none (0); the scheduler asks it instead
// of choosing "fault.nemesis" by index.
func (s *Script) ChooseNemesis(enabled []string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, name := range enabled {
		if j := s.due("nemesis." + name); j >= 0 {
			s.done[j] = true
			return i + 1
		}
	}
	return 0
}

// Entropy implements Entropy: Base's, else zeros (the nominal schedule).
func (s *Script) Entropy(n int) []byte {
	if e := EntropyOf(s.Base); e != nil {
		return e(n)
	}
	return make([]byte, n)
}

// Remaining are the scripted faults that have not happened.
func (s *Script) Remaining() []Scripted {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Scripted
	for i, f := range s.Faults {
		if s.done == nil || !s.done[i] {
			out = append(out, f)
		}
	}
	return out
}

// nemesisChooser chooses a nemesis by name (Script).
type nemesisChooser interface{ ChooseNemesis(enabled []string) int }

// nemesisOf returns c's choice of a nemesis by name, through the wrappers
// of this package (a Recorder records it as "fault.nemesis"), or nil.
func nemesisOf(c Chooser) func(enabled []string) int {
	for c != nil {
		switch x := c.(type) {
		case *ChoiceLog: // records the choice it passes on
			by := nemesisOf(x.C)
			if by == nil {
				return nil
			}
			return func(enabled []string) int {
				v := by(enabled)
				c := Choice{Kind: "fault.nemesis", N: len(enabled) + 1, Value: v}
				if v > 0 {
					c.Name = enabled[v-1]
				}
				x.Choices = append(x.Choices, c)
				return v
			}
		case nemesisChooser:
			return x.ChooseNemesis
		}
		w, ok := c.(Wrapper)
		if !ok {
			return nil
		}
		c = w.Unwrap()
	}
	return nil
}

// Unwrap implements Wrapper: a Script keeps its Base's context.
func (s *Script) Unwrap() Chooser { return s.Base }
