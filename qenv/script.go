package qenv

import (
	"fmt"
	"strings"
	"sync"
)

// A Scripted fault is one fault a Script makes happen: at the first choice
// of Kind ("fault.<kind>", or "nemesis.<name>" for a nemesis such as
// "nemesis.crash.payments") once the run has taken After steps.
type Scripted struct {
	Kind   string
	Option int // the fault choice's option (ignored for a nemesis)
	After  int
}

func (s Scripted) String() string {
	return fmt.Sprintf("%s=%d after %d steps", s.Kind, s.Option, s.After)
}

// Script is a Chooser that makes exactly the faults it is given, in order,
// and no others: a model trace's crashes and failures imposed on the
// program (connect.FaultScript), while the program does the rest. Every
// other fault choice is nominal, and non-fault choices are Base's (nil:
// nominal, the canonical schedule).
//
// Progress tells how many steps the run has taken (the length of its
// recorder's steps): a scripted fault waits for the steps before it in the
// trace, so it lands where the trace put it. A fault the run never comes
// to stays in Remaining, and the run did not follow the trace.
type Script struct {
	Faults   []Scripted
	Base     Chooser
	Progress func() int

	mu   sync.Mutex
	next int
}

func (s *Script) due(kind string) bool {
	if s.next >= len(s.Faults) || s.Faults[s.next].Kind != kind {
		return false
	}
	return s.Progress == nil || s.Progress() >= s.Faults[s.next].After
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
	if !s.due(k) {
		return 0
	}
	v := s.Faults[s.next].Option
	if v <= 0 || v >= n {
		panic(fmt.Sprintf("qenv: script fault %v: %s has %d options", s.Faults[s.next], kind, n))
	}
	s.next++
	return v
}

// ChooseNemesis picks a nemesis by name among the enabled ones: the
// scripted one if it is due, else none (0); the scheduler asks it instead
// of choosing "fault.nemesis" by index.
func (s *Script) ChooseNemesis(enabled []string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, name := range enabled {
		if s.due("nemesis." + name) {
			s.next++
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
	return append([]Scripted(nil), s.Faults[s.next:]...)
}

// nemesisChooser chooses a nemesis by name (Script).
type nemesisChooser interface{ ChooseNemesis(enabled []string) int }

// nemesisOf returns c's choice of a nemesis by name, through the wrappers
// of this package (a Recorder records it as "fault.nemesis"), or nil.
func nemesisOf(c Chooser) func(enabled []string) int {
	switch c := c.(type) {
	case ctxChooser:
		return nemesisOf(c.Chooser)
	case *Recorder:
		if by := nemesisOf(c.C); by != nil {
			return func(enabled []string) int {
				v := by(enabled)
				c.Choices = append(c.Choices, Choice{Kind: "fault.nemesis", N: len(enabled) + 1, Value: v})
				return v
			}
		}
	case *swarm:
		return nemesisOf(c.c)
	case nemesisChooser:
		return c.ChooseNemesis
	}
	return nil
}
