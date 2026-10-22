---
name: quint-mbt-driver
description: Write, extend or debug a quint-connect-go model-based test in Go, where Quint traces drive the implementation (connect.Run, connect.Test, connect.Switch, StateDriver, Pick). Use when asked to test Go code against a Quint spec "like quint-connect", when a connect test reports an unimplemented action or a state divergence, or when adding a new action to a driver.
---

# Model-based testing: Quint drives Go (package `connect`)

`connect` is the Go counterpart of the Rust quint-connect crate:
`quint run --mbt` generates random traces of the model; each trace is
replayed on a fresh driver; after every step the implementation's state is
compared with the model's.

## Skeleton

```go
type driver struct{ sut *Bank }

func (d *driver) Step(s *connect.Step) error {
	return connect.Switch{
		"init":    func(*connect.Step) error { d.sut = NewBank(); return nil },
		"deposit": func(s *connect.Step) error {
			return d.sut.Deposit(connect.MustPick[string](s, "account"), connect.MustPick[int](s, "amount"))
		},
		// "otherComponentAction": connect.Skip,
	}.Step(s)
}

// Compared after every step, init included. Only the fields of this type
// are compared: project the part of the model the implementation has.
type state struct{ Balances map[string]int }

func (d *driver) State() (any, error) { return state{Balances: d.sut.Balances()}, nil }

func TestModel(t *testing.T) {
	connect.Run(t, connect.Config{Spec: "model/bank.qnt", Main: "bankDesign", MaxSamples: 50, MaxSteps: 30},
		func() connect.Driver { return &driver{} })
}
```

Mapping from quint-connect: `#[quint_run]` → `connect.Run`; `#[quint_test]`
→ `connect.Test`; `switch!` → `connect.Switch` (key `"_"` is the catch-all);
`param?` → `connect.OptPick[T]`; `impl State` → `State() (any, error)`;
`Config{state, nondet}` → `Config.StatePath`, `Config.NondetPath`.

## Decoding model values (`itf.Decode`, used by Pick and State)

- Quint `int` → any Go integer (overflow is an error) or `*big.Int`.
- `str`, `bool` → `string`, `bool`.
- `List`, `Set`, tuple → slice or array (sets in canonical order); a `Set[T]`
  also decodes into `map[T]bool`.
- `a -> b` → `map[A]B`; a record → struct (fields matched by `itf:"name"`
  tag, else by name ignoring case and `_`; extra model fields are ignored).
- An enum (`type Phase = A | B`) → a `string` type; `Some(x)`/`None` → a
  pointer.
- A sum type with payloads → a struct with `Tag string \`itf:",tag"\`` and a
  `\`itf:",value"\`` field, or a field named like each variant.
- A type with `UnmarshalITF(itf.Value) error` decodes itself.

## The TDD loop (one action at a time)

1. Run the test. `unimplemented action "X" (picks: a, b)` names the next
   action to implement and the picks available.
2. Add the `"X"` case: decode the picks the action's parameters use, call
   the implementation, and return its error.
3. Run again. On `states diverge`, read the diff:
   `.Balances[bob]: model 16, implementation 17`, which is printed with the
   step, its picks and the model's next state.
   - If the model's state is right, fix the code.
   - If the projection is wrong (a field mapped incorrectly), fix `State()`.
   - The spec is ground truth: change it only when the requirements say
     the model is wrong, and say so.
4. Repeat until a run with `MaxSamples` ≥ 50 passes. Then raise `MaxSteps`.

## Reproducing and debugging

- A failure prints `reproduce with QUINT_SEED=0x...`. These variables are
  read at run time, with no rebuild: `QUINT_SEED`, `QUINT_VERBOSE=1|2`
  (log every step, or every state too), `QUINT_MAX_SAMPLES`,
  `QUINT_MAX_STEPS`, `QUINT_BACKEND=rust|typescript`, `QUINT_BIN`.
- Each trace gets a fresh driver, but `init` still runs first: reset there
  too if the driver holds external resources.
- A handler's panic (a failed `MustPick`, an assertion) fails that step with
  its message.
- For something that is not a plain state comparison (learned id mappings,
  reading a store), implement `CheckStep(s *connect.Step) error`
  (`connect.StepChecker`).
- For concurrent implementations, make the effects schedulable: put a gate
  at the effect seam (each write blocks until the handler of that model step
  releases it). See `examples/edgepublish/mbt`.

## Driving a whole program through its faults (`connect.FaultScript`)

A driver calls the code once per step. For a program that runs by
itself (a process with its own loop), let the trace choose only the
environment: its failures and crashes.

```go
sc, err := connect.FaultScript(tr, connect.FaultMap{
	"writeFail": func(*connect.Step) *connect.Fault { return &connect.Fault{Kind: "journal.write", Option: 1, Recorded: true} },
	"restart":   func(*connect.Step) *connect.Fault { return &connect.Fault{Kind: "nemesis.crash.writer", Recorded: true} },
}, connect.ReplayOptions{})
s := qenv.New(qenv.WithContext(sc, qobs.WithRecorder(ctx, qobs.NewRecorder(mem))))
// ... deploy, s.Run, then: sc.Remaining() is empty, and
qtest.Check(t, binding, mem.Steps())
```

- Each mapped step is a fault: a fault point's or a double's kind and
  option, or `nemesis.crash.<process>`. It fires at the first choice of
  its kind once the run has recorded the steps before it in the trace.
  No other fault happens, and the schedule is nominal (or `sc.Base`'s).
- `Recorded` says whether the program records the step itself (a failed
  write, a restart). Steps it does not record don't count toward the
  next fault's wait.
- A fault left in `Remaining()`, or recorded actions that differ from
  the trace, mean the program could not follow it. A model check that
  fails means it followed the trace's faults but did the wrong thing
  (connect/faults_test.go: a restart that forgot the durable count).

## Limits

- Model-driven testing never exercises what the model forbids: the model
  does not take disabled steps. To find code that accepts forbidden inputs,
  let Hegel drive and the model judge (see the quint-hegel skill).
- `quint test` traces have no `mbt::` annotations. Use `connect.Run`, or
  `NondetPath` when the spec records the action in its state (Choreo's
  `extensions.actionTaken`).
- When the model chooses where the implementation decides on its own
  (the model leases any pending job, the queue always the oldest), model
  traces ask for steps the code cannot be made to take. Either make the
  choice controllable (a test hook), or check the code's own runs instead
  (trace validation, Hegel). See `examples/jobqueue`.
