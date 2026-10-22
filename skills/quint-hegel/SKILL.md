---
name: quint-hegel
description: Combine Hegel property-based / state-machine testing (hegel.dev/go/hegel) with a Quint model using quint-connect-go's quinthegel package - Hegel-driven runs judged and shrunk by the model, or Quint traces replayed inside Hegel with drawn concrete details. Use when writing hegel.RunStateful machines for code that has a Quint spec, when a Hegel counterexample must be classified as an implementation or design bug, or when model-based tests miss inputs the model forbids.
---

# Hegel + Quint (`quinthegel`)

Hegel generates and shrinks test cases. Quint knows what the system may
do. They can be combined in two directions:

| | Hegel drives, model judges (`quinthegel.Test`, `CheckCase`) | Model drives, Hegel varies (`quinthegel.Replay`) |
|---|---|---|
| Who chooses the operations | Hegel rules (`Rule*` methods), invalid inputs included | the model's traces (`quint run --mbt`) |
| Finds | the code doing what the model forbids (accepting a forbidden request), wrong states, invariant violations | divergence on behaviours the model allows, under concrete variations the model abstracts (payloads, timing, noise) |
| Shrinks to | the shortest run the model rejects | the simplest trace, shortest prefix, simplest details |
| Cost | one `quint test` per checked run (~0.5–1 s), cached across shrinking | one `quint run` per test binary; replay is in-process |

Use both. Model-driven tests alone cannot find what the model forbids,
because the model never asks for a disabled step.

## Hegel drives, model judges

1. **Recording.** The code records model steps, through `qobs.Record` in
   the implementation, a woven `//quint:action`, or `rec.Step(...)` in the
   rule.
2. **Binding.** A binding maps the steps to the model (see the
   quint-trace-validation skill).

```go
type machine struct{ bank *Bank }

func (m *machine) RuleTransfer(tc hegel.TestCase) {
	_ = m.bank.Transfer(ctx, hegel.Draw(tc, anyAccount), hegel.Draw(tc, anyAccount), hegel.Draw(tc, hegel.Integers(-1, 6)))
}
func (m *machine) InvariantNoOverdraft(tc hegel.TestCase) { /* the plain PBT property, optional */ }

func TestBankAgainstModel(t *testing.T) {
	quinthegel.Test(t, quinthegel.Config{Binding: "bank.binding.yaml", Mode: quinthegel.Each, Global: true},
		func(ht *hegel.T, rec *quinthegel.Recorder) {
			hegel.RunStateful(ht, &machine{bank: New(accounts)}, hegel.WithStatefulStepCount(12))
		}, hegel.WithTestCases(30))
}
```

- **Modes:** `Batched` is the usual choice (bulk checks, replayed in
  `Each` on a failure so Hegel shrinks it); `Each` checks every case;
  `After` checks once at the end and shrinks nothing. `Global: true`
  also installs each case's recorder globally, for steps from goroutines
  the body starts (not in parallel tests). Read [references/modes.md](references/modes.md) for the
  modes' costs and options, generators and budgets.
- **Expected failures** (a mutant, a known bug): use
  `quinthegel.FindViolation`. It checks every case with the verdict
  cache, lets Hegel shrink, fails the test if every case passes, and
  returns the shrunk run; state how it must show with `Expect`:
  ```go
  v := quinthegel.FindViolation(t, quinthegel.Config{Binding: "bank.binding.yaml", Global: true},
  	func(tc hegel.TestCase, _ *quinthegel.Recorder) {
  		hegel.RunStateful(tc, &machine{bank: New(accounts, WithMutation(SelfTransfer))})
  	}, hegel.WithTestCases(50), hegel.WithSeed(3))
  v.Expect(t, qtest.Rejects("transfer"))
  ```
  (`hegel.Test` cannot express "expected to fail". `FindViolation`
  wraps `hegel.Run`, `CaseChecker` and the TooSlow suppression.) Hegel
  runs quiet: the expected failure's replay, stack and reproduction blob
  are shown only if the outcome is not the expected one
  (`QUINTGO_VERBOSE=1` shows them always). A System shrinks the same way:
  `quinthegel.Shrink(t, sys).Expect(t, qtest.Rejects("apply"))`, and
  `.Expect(t, qtest.Conforms())` states that Hegel finds nothing.

## Model drives, Hegel varies

```go
traces := quinthegel.Traces(t, connect.Config{Spec: "model/bank.qnt", Main: "bankDesign", MaxSamples: 30, MaxSteps: 20})
hegel.Test(t, func(ht *hegel.T) {
	quinthegel.Replay(ht, traces, func(tc hegel.TestCase) connect.Driver {
		return &noisyDriver{tc: tc} // handlers may hegel.Draw concrete details
	}, quinthegel.ReplayOptions{Prefix: true})
})
```

The driver is an ordinary `connect.Driver`. With `State()` it is also
compared with the model after every step.

## Classifying a counterexample

Validate the shrunk run (the `Each` failure message already contains the
report):
- **Does not conform** (a disabled step, or a state mismatch): the
  implementation did something the design does not allow. Fix the code.
- **Conforms but a model invariant fails**: the design itself allows the
  bad behaviour. Fix the design, then the code. The run is a witness for
  the model, and its generated module is a ready-made `quint test`
  regression.
- **Conforms and satisfies the model, but a Hegel invariant fails**: the
  Hegel property is stronger than the model. Either the model is missing
  a property (add a `val`), or the test's property is wrong.

## Pitfalls

Concurrency within a case, empty cases, the Go version, and faults for
distributed systems (`hegelnet.Network`): read
[references/pitfalls.md](references/pitfalls.md) before writing a
machine for concurrent or distributed code.
