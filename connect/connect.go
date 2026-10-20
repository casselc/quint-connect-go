// Package connect drives Go code from Quint traces: model-based testing in
// the style of the Rust quint-connect crate (github.com/quint-co/quint-connect).
//
// Quint generates traces, by random simulation (`quint run --mbt`, see Run)
// or from a named `run` in the spec (`quint test`, see Test). A Driver
// replays each step on the implementation; after every step, a StateDriver's
// state is compared with the model's.
//
//	type bank struct{ b *Bank }
//
//	func (d *bank) Step(s *connect.Step) error {
//		return connect.Switch{
//			"init":     func(*connect.Step) error { d.b = NewBank(); return nil },
//			"deposit":  func(s *connect.Step) error { return d.b.Deposit(connect.MustPick[int](s, "amount")) },
//			"withdraw": func(s *connect.Step) error { return d.b.Withdraw(connect.MustPick[int](s, "amount")) },
//		}.Step(s)
//	}
//
//	// State is compared with the model's state after every step.
//	func (d *bank) State() (any, error) { return BankState{Balance: d.b.Balance()}, nil }
//
//	func TestBank(t *testing.T) {
//		connect.Run(t, connect.Config{Spec: "bank.qnt", MaxSteps: 20}, func() connect.Driver { return &bank{} })
//	}
//
// The quint-connect concepts map as follows:
//
//	#[quint_run(spec, main, init, step, max_samples, max_steps, seed)]  connect.Run(t, Config{...}, newDriver)
//	#[quint_test(spec, test, main, max_samples, seed)]                   connect.Test(t, Config{...}, "testName", newDriver)
//	impl Driver { fn step(&mut self, &Step) }                            Driver.Step(*Step) error
//	switch!(step { init => ..., A(x, y?) => ... })                       connect.Switch{"init": ..., "A": ...} with Pick/OptPick
//	impl State<D> (serde Deserialize + PartialEq)                        StateDriver.State() (any, error), decoded with itf.Decode
//	Config { state: &[...], nondet: &[...] }                             Config.StatePath, Config.NondetPath
//	QUINT_SEED, QUINT_VERBOSE (compile time)                             QUINT_SEED, QUINT_VERBOSE, ... (read at run time)
//
// Differences: a fresh Driver is made for every trace (no state leaks
// between traces); traces run in a deterministic order; a state mismatch
// always prints a field-level diff; and the environment is read when the
// test runs, so changing QUINT_SEED needs no rebuild.
package connect

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/casselc/quint-connect-go/itf"
)

// Step is what a driver sees: the action the model took, its nondeterministic
// picks, and the model's state before and after it. State 0 is the "init"
// step: Before is nil and After is the initial state.
type Step struct {
	Trace  int    // index of the trace being replayed
	Index  int    // index of the state in the trace (0 = init)
	Action string // mbt::actionTaken, or the tag of Config.NondetPath
	// Picks are the nondet values chosen on the way to this state, by the
	// name of the `nondet` binding. Picks quint reports as None are absent.
	Picks  map[string]itf.Value
	Before map[string]itf.Value
	After  map[string]itf.Value
	// State is the part of After that a StateDriver is compared with
	// (Config.StatePath; the whole state when the path is empty).
	State itf.Value
}

// Has reports whether a pick was made in this step.
func (s *Step) Has(name string) bool { _, ok := s.Picks[name]; return ok }

// Pick returns a nondet pick as an ITF value; it panics if absent. Prefer
// the typed Pick[T] / MustPick[T].
func (s *Step) Pick(name string) itf.Value {
	v, ok := s.Picks[name]
	if !ok {
		panic(fmt.Sprintf("step %d (%s): unknown nondet pick %q (have %s)", s.Index, s.Action, name, s.pickNames()))
	}
	return v
}

// Get navigates the post-state: s.Get("writer", "pushes").
func (s *Step) Get(path ...string) itf.Value {
	if len(path) == 0 {
		return itf.Value{Kind: itf.Record, Fields: s.After}
	}
	v := s.After[path[0]]
	for _, p := range path[1:] {
		v = v.Get(p)
	}
	return v
}

func (s *Step) pickNames() string {
	var ks []string
	for k := range s.Picks {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	if len(ks) == 0 {
		return "no picks"
	}
	return strings.Join(ks, ", ")
}

// String renders the step as quint-connect does: the action, the picks and
// the next state, in Quint syntax.
func (s *Step) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Action taken: %s\n", s.Action)
	if len(s.Picks) > 0 {
		b.WriteString("Nondet picks:\n")
		for _, k := range sortedKeys(s.Picks) {
			fmt.Fprintf(&b, "  + %s: %s\n", k, s.Picks[k])
		}
	}
	b.WriteString("Next state:\n")
	for _, k := range sortedKeys(s.After) {
		fmt.Fprintf(&b, "  + %s: %s\n", k, s.After[k])
	}
	return b.String()
}

func (s *Step) summary() string {
	var b strings.Builder
	b.WriteString(s.Action)
	if len(s.Picks) > 0 {
		b.WriteString("(")
		for i, k := range sortedKeys(s.Picks) {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "%s=%s", k, s.Picks[k])
		}
		b.WriteString(")")
	}
	return b.String()
}

// Pick decodes a nondet pick into T (see itf.Decode). A missing pick is an
// error, like a required parameter of quint-connect's switch!.
func Pick[T any](s *Step, name string) (T, error) {
	var t T
	v, ok := s.Picks[name]
	if !ok {
		return t, fmt.Errorf("unknown nondet pick %q in %s (have %s)", name, s.Action, s.pickNames())
	}
	if err := itf.Decode(v, &t); err != nil {
		return t, fmt.Errorf("pick %s: %w", name, err)
	}
	return t, nil
}

// OptPick decodes an optional pick (`param?` in switch!): nil when the model
// did not pick it in this step.
func OptPick[T any](s *Step, name string) (*T, error) {
	if _, ok := s.Picks[name]; !ok {
		return nil, nil
	}
	t, err := Pick[T](s, name)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// MustPick is Pick, panicking on error. Replay turns a handler's panic into
// a step failure, so handlers can use it freely.
func MustPick[T any](s *Step, name string) T {
	t, err := Pick[T](s, name)
	if err != nil {
		panic(err)
	}
	return t
}

// Driver replays model steps on the implementation.
type Driver interface {
	Step(s *Step) error
}

// StateDriver is a Driver whose state is compared with the model's after
// every step, init included. State returns the implementation's state as a
// Go value; the model's state (Step.State) is decoded into a new value of
// the same type with itf.Decode, and the two must be reflect.DeepEqual.
// Fields absent from the Go type are not compared, so the type can project
// the part of the model the implementation has.
type StateDriver interface {
	Driver
	State() (any, error)
}

// StepChecker is a Driver with a free-form check after every step, e.g. a
// comparison that needs the learned mapping between implementation ids and
// model values.
type StepChecker interface {
	Driver
	CheckStep(s *Step) error
}

// Handler runs one model action on the implementation.
type Handler func(s *Step) error

// Switch dispatches on the action name: the analogue of quint-connect's
// switch! macro. The key "_" is the catch-all. An action with no handler is
// an "unimplemented action" error, which is how the TDD loop finds the next
// action to implement.
type Switch map[string]Handler

func (sw Switch) Step(s *Step) error {
	if h, ok := sw[s.Action]; ok {
		return h(s)
	}
	if h, ok := sw["_"]; ok {
		return h(s)
	}
	return &UnimplementedError{Action: s.Action, Picks: s.pickNames()}
}

// Skip is a Handler for actions the implementation does not have (another
// component's actions, stuttering steps).
func Skip(*Step) error { return nil }

// DriverFunc adapts a function to a Driver.
type DriverFunc func(s *Step) error

func (f DriverFunc) Step(s *Step) error { return f(s) }

// UnimplementedError is returned by Switch for an action it has no handler for.
type UnimplementedError struct{ Action, Picks string }

func (e *UnimplementedError) Error() string {
	return fmt.Sprintf("unimplemented action %q (picks: %s): add a handler for it", e.Action, e.Picks)
}

// ReplayOptions say how to read a trace.
type ReplayOptions struct {
	// StatePath is the path, from the top-level variables, to the state a
	// StateDriver is compared with; empty means all variables. Module
	// qualifiers are stripped from variable names ("m::choreo::s" is "s").
	StatePath []string
	// NondetPath, if set, is the path to a variable holding a sum type whose
	// tag is the action and whose payload record holds the picks, for specs
	// that record the action themselves (Choreo's extensions.actionTaken),
	// and for `quint test` traces, which carry no mbt:: variables.
	NondetPath []string
	// Trace is the trace's index, reported in Step.Trace and in errors.
	Trace int
	// OnStep is called before each step is driven, e.g. to log it.
	OnStep func(s *Step)
}

// StepError is a failed step of a replay.
type StepError struct {
	Trace int
	Step  *Step
	Kind  string // "step" (the handler failed), "state" (states diverge), "check", "trace" (malformed trace)
	Err   error
	Diff  []string // for Kind "state": the differing fields, model vs implementation
}

func (e *StepError) Error() string {
	var b strings.Builder
	switch e.Kind {
	case "state":
		fmt.Fprintf(&b, "trace %d step %d %s: specification and implementation states diverge", e.Trace, e.Step.Index, e.Step.summary())
		for _, d := range e.Diff {
			fmt.Fprintf(&b, "\n    %s", d)
		}
		if e.Err != nil {
			fmt.Fprintf(&b, "\n    %v", e.Err)
		}
	case "trace":
		fmt.Fprintf(&b, "trace %d state %d: %v", e.Trace, e.Step.Index, e.Err)
	default:
		fmt.Fprintf(&b, "trace %d step %d %s: %v", e.Trace, e.Step.Index, e.Step.summary(), e.Err)
	}
	return b.String()
}

func (e *StepError) Unwrap() error { return e.Err }

// Replay drives one trace through d. It stops at the first failing step and
// returns a *StepError.
func Replay(tr *itf.Trace, d Driver, opt ReplayOptions) error {
	if len(tr.States) == 0 {
		return fmt.Errorf("trace %d: empty trace", opt.Trace)
	}
	sd, _ := d.(StateDriver)
	sc, _ := d.(StepChecker)
	var before map[string]itf.Value
	for i, st := range tr.States {
		s := &Step{Trace: opt.Trace, Index: i, Before: before, After: st.Vars, Picks: st.Picks, Action: st.Action}
		before = st.Vars
		fail := func(kind string, err error, diff []string) error {
			return &StepError{Trace: opt.Trace, Step: s, Kind: kind, Err: err, Diff: diff}
		}
		if len(opt.NondetPath) > 0 {
			act, picks, err := nondetAt(st.Vars, opt.NondetPath)
			if err != nil {
				return fail("trace", err, nil)
			}
			s.Action, s.Picks = act, picks
		} else if s.Action == "" {
			if !hasMBT(tr) {
				return fail("trace", fmt.Errorf("the trace has no mbt::actionTaken: generate it with `quint run --mbt`, or set NondetPath to a variable that records the action (traces of `quint test` carry no mbt:: variables)"), nil)
			}
			if i == 0 {
				s.Action = "init"
			} else {
				return fail("trace", fmt.Errorf("an anonymous action was taken: name every action that `step` can take (e.g. `action a_and_b = all { a, b }` instead of an inline all { ... })"), nil)
			}
		}
		state, err := walk(st.Vars, opt.StatePath)
		if err != nil {
			return fail("trace", err, nil)
		}
		s.State = state
		if opt.OnStep != nil {
			opt.OnStep(s)
		}
		if err := call(d, s); err != nil {
			return fail("step", err, nil)
		}
		if sd != nil {
			impl, err := sd.State()
			if err != nil {
				return fail("state", fmt.Errorf("State(): %w", err), nil)
			}
			if diff, err := compare(state, impl); err != nil || len(diff) > 0 {
				return fail("state", err, diff)
			}
		}
		if sc != nil {
			if err := sc.CheckStep(s); err != nil {
				return fail("check", err, nil)
			}
		}
	}
	return nil
}

func hasMBT(tr *itf.Trace) bool {
	for _, v := range tr.Vars {
		if v == "mbt::actionTaken" {
			return true
		}
	}
	for _, st := range tr.States {
		if st.Action != "" {
			return true
		}
	}
	return false
}

// call runs a handler, turning a panic (MustPick, a failed assertion) into an error.
func call(d Driver, s *Step) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(error); ok {
				err = fmt.Errorf("panic: %w", e)
			} else {
				err = fmt.Errorf("panic: %v", r)
			}
		}
	}()
	return d.Step(s)
}

func walk(vars map[string]itf.Value, path []string) (itf.Value, error) {
	v := itf.Value{Kind: itf.Record, Fields: vars}
	for i, p := range path {
		if v.Kind != itf.Record {
			return v, fmt.Errorf("state path %s: %s is not a record", strings.Join(path, "."), strings.Join(path[:i], "."))
		}
		next, ok := v.Fields[p]
		if !ok && i == 0 {
			if j := strings.LastIndex(p, "::"); j >= 0 {
				next, ok = v.Fields[p[j+2:]]
			}
		}
		if !ok {
			return v, fmt.Errorf("state path %s: no %q (have %s)", strings.Join(path, "."), p, strings.Join(sortedKeys(v.Fields), ", "))
		}
		v = next
	}
	return v, nil
}

func nondetAt(vars map[string]itf.Value, path []string) (string, map[string]itf.Value, error) {
	v, err := walk(vars, path)
	if err != nil {
		return "", nil, err
	}
	if v.Kind != itf.Variant {
		return "", nil, fmt.Errorf("nondet path %s: want a sum type value, have %s", strings.Join(path, "."), v)
	}
	picks := map[string]itf.Value{}
	if v.Inner != nil {
		switch v.Inner.Kind {
		case itf.Record:
			for k, x := range v.Inner.Fields {
				if x.Kind == itf.Variant && x.Tag == "None" {
					continue
				}
				if x.Kind == itf.Variant && x.Tag == "Some" && x.Inner != nil {
					x = *x.Inner
				}
				picks[k] = x
			}
		case itf.Tuple:
			if len(v.Inner.Elems) > 0 {
				return "", nil, fmt.Errorf("nondet path %s: the variant's payload must be a record of picks, have %s", strings.Join(path, "."), v.Inner)
			}
		default:
			return "", nil, fmt.Errorf("nondet path %s: the variant's payload must be a record of picks, have %s", strings.Join(path, "."), v.Inner)
		}
	}
	return v.Tag, picks, nil
}

// compare decodes the model state into the implementation state's type and
// diffs the two.
func compare(model itf.Value, impl any) ([]string, error) {
	if impl == nil {
		return nil, fmt.Errorf("State() returned nil")
	}
	t := reflect.TypeOf(impl)
	want := reflect.New(t)
	if err := itf.Decode(model, want.Interface()); err != nil {
		return nil, fmt.Errorf("decoding the model's state into %s: %w", t, err)
	}
	return Diff(want.Elem().Interface(), impl), nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
