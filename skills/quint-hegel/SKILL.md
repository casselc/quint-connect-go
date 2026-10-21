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

- **Modes:**
  - `Batched` is the usual choice: rounds of unchecked generation, bulk
    checks (many runs per quint process), and on a failure a replay of
    the round in `Each` mode, so Hegel shrinks it.
    - All passing, it costs about what `After` does (3.4 s against 22 s
      for `Each` on 40 cases).
    - Set `Rounds` or `Budget` to keep exploring, and `Seed` to
      reproduce.
    - The body must be deterministic in Hegel's draws.
  - `Each` checks every case, so a violation is a Hegel failure and Hegel
    shrinks it against the model. A `CaseChecker` caches verdicts, so
    shrinking runs quint far less than once per candidate.
  - `After` checks the distinct runs in bulk after Hegel finishes. It is
    the cheapest, but shrinks nothing.
  - `Off` records only.
- **`Global: true`** installs each case's recorder as the global qobs
  recorder, for implementation-side `qobs.Record` or woven code. Rules
  can also call `rec.Step(action, err, kv...)`. Every step becomes a Hegel
  note, so the shrunk run prints as model actions.
- **Expected failures** (a mutant, a known bug): use
  `quinthegel.FindViolation`. It checks every case with the verdict
  cache, lets Hegel shrink, fails the test if every case passes, and
  returns the shrunk run to assert on:
  ```go
  v := quinthegel.FindViolation(t, quinthegel.Config{Binding: "bank.binding.yaml", Global: true},
  	func(tc hegel.TestCase, _ *quinthegel.Recorder) {
  		hegel.RunStateful(tc, &machine{bank: New(accounts, WithMutation(SelfTransfer))})
  	}, hegel.WithTestCases(50), hegel.WithSeed(3))
  if v.Last().Action != "transfer" { t.Fatalf("...%s", quinthegel.Render(v.Steps)) }
  ```
  (`hegel.Test` cannot express "expected to fail". `FindViolation`
  wraps `hegel.Run`, `CaseChecker` and the TooSlow suppression.)
- **Generators.** Draw invalid inputs too: unknown ids, non-positive
  amounts, duplicates. Map refused calls to `outcomes: { error: skip }` in
  the binding. A refused call is then no model step, but a wrongly
  accepted one becomes a step the model rejects.
- **Budget.** Keep `WithTestCases` modest (20–50) and
  `WithStatefulStepCount` around 8–15 in `Each` mode. Skip the long
  shrinking tests under `testing.Short()`.

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

- Hegel runs cases sequentially. With `hegel.WithConcurrency`, the rules
  of one case share its recorder. Record under the lock that orders the
  effect, so the recording order is the effect order.
- A test case that records nothing is vacuously fine: `AllowEmpty`, and
  `CaseChecker` treats it as OK.
- `hegel-go` needs Go ≥ 1.26.1. Its engine is embedded (no Python, no
  server), and runs offline after the first download.
- For distributed systems, embed `maelstrom.Network` (examples/glomers)
  in the machine. Hegel then schedules deliveries, losses, partitions
  and timers next to the client requests. Settle at the end of each case
  (heal, quiesce, record `settle`), so properties conditioned on
  quiescence are checked.
