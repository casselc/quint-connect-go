---
name: quint-verification-workflow
description: End-to-end workflow for checking a Go system against a Quint model with quint-connect-go - model first, simulate its invariants, implement with step recording, bind, prove each check can fail with mutants (qtest.Expect, quinthegel.FindViolation), let Hegel explore, then check runs from the real environment - and how to triage each verdict. Use when starting verification of a new component or example, planning which tests to write, deciding whether a failure is the code's, the model's or the binding's, or setting up CI for model-checked tests.
---

# Verifying a Go system against a Quint model

The order matters. Each stage checks the previous one, and skipping one
leaves a class of mistakes undetected. The other skills cover each
stage's mechanics: quint-model-for-go, quint-modeling-patterns,
quint-trace-validation, quint-mbt-driver and quint-hegel.

## 1. Model and simulate before writing code

```sh
quint typecheck model/m.qnt
quint run model/m.qnt --main design --invariant prop --max-steps 30 --max-samples 2000
```

- Every property must hold in simulation before any code exists. A
  property that fails here is a design bug, or a wrong property.
- Also check that each property *can* fail. Temporarily break the model
  (drop a guard), or write a `run` test that reaches a violating state.
  A property that never fails checks nothing.
- Add `run ...Test` scenarios for the behaviours you care about, so the
  model has regression tests of its own.

## 2. Implement with recording

Record each model step where it takes effect (quint-modeling-patterns §3):
`qobs.Record(ctx, "action", err, "param", v, ..., "obs.x", observed)`.
Name step fields after the model's parameters: the binding then needs no
template for them.

## 3. Bind and lint

```sh
quintgo scaffold -spec model/m.qnt -module m > m.binding.yaml   # parameters default to same-named fields
quintgo lint -binding m.binding.yaml -src ./pkg
quintgo scaffold -system -binding m.binding.yaml -pkg m > system_test.go   # a System test: fill in Run, then a mutant
```

Add only what isn't a same-named field:
- an `expect` per observation;
- `outcomes` for refusals;
- `restart`;
- `constants`;
- the promised `invariants`.

## 4. Prove each check can fail: mutants

A conforming verdict means nothing until you have seen the same test
reject a bug. For each property and each `expect`:
1. Add a mutant to the implementation: a `Mutation` option, never a
   build tag, so one binary has them all.
2. Write the test that states *how* the mutant must show up. "It fails"
   is not enough: a binding error also fails.

```go
qtest.Record(t, "m.binding.yaml", scenario(Faithful)).Expect(t, qtest.Conforms())
qtest.Record(t, "m.binding.yaml", scenario(LostWrite)).Expect(t, qtest.Mismatch("write"))   // expect caught it
qtest.Record(t, "m.binding.yaml", scenario(AckAny)).Expect(t, qtest.Rejects("ack"))         // a step the model can't take
qtest.Record(t, "m.binding.yaml", scenario(NoRetry)).Expect(t, qtest.Violates("converged")) // the design property
```

Keep a table in the README: mutant, check, how it shows up. If no test
catches a mutant, the model or the binding is too weak. Strengthen it, or
document the gap (as glomers does for the scheme model).

## 5. Let Hegel explore

- **Faithful code.** `quinthegel.Test` in `Batched` mode: cheap while
  everything passes, and it shrinks on failure.
- **Mutants.**
  `v := quinthegel.FindViolation(t, cfg, body, hegel.WithSeed(n))`
  returns the shrunk run; state how it shows with
  `v.Expect(t, qtest.Rejects("transfer"))`, not by reading its
  summary (`quinthegel.Shrink(t, sys)` for a System). Skip it under `testing.Short()`: shrinking runs quint
  dozens of times.
- **Faults.** For distributed code, embed `hegelnet.Network`, or your
  own fault rules, so Hegel schedules losses, partitions, timers and
  restarts. For services with stores, queues and remote calls, deploy
  them into a `qenv` simulation (the quint-fault-injection skill). Then
  explore seeds with `qtest.Explore`, and shrink with `quinthegel.Chooser`.

State what a set of runs must show with claims:
- an expectation alone is about some run (`Conforms()` about every run);
- several are judged one by one;
- `qtest.Some(a, b)` wants one run showing both;
- `qtest.All(e)` wants every run;
- `qtest.None(e)` wants no run: what a bug must not do, or what a row
  without a fault must never show.

A shrunk Hegel failure is stated the same way:
`quinthegel.FindViolation(...).Expect(t, qtest.Rejects("ship"))`. Don't
match strings in its summary.

Hegel finds what scripted scenarios don't (glomers kafka: a commit past
the end of the log). When it does, add the shrunk run as a scripted
regression.

## 6. Check the real environment

Run the system where it will run (Maelstrom, a staging cluster, a soak
test). Write step logs (`qobs.FileSink`) or OTel span events, and
validate them:

```sh
quintgo validate -noitf -binding m.binding.yaml logs/*.jsonl
```

The model can be wrong about the environment (quint-modeling-patterns
§2). Only the real environment tells you.

## Triage: whose bug is it?

Read the report: it shows `expect:`, `obs:` and the model's state around
the failing step.

| Verdict | First suspect | Then |
|---|---|---|
| step not a transition (`Rejects`) | the code did something the model forbids | the binding: wrong argument mapping, a refusal not mapped to `skip`, a missing `restart` |
| state mismatch (`Mismatch`) | the code computed a different state | the `expect` or the recorded `obs.*` value is wrong, or recorded at the wrong moment |
| conforms, invariant FAIL (`Violates`) | the design allows the bad behaviour | the property is stronger than the requirement |
| tool error (exit 2) | the binding or the model doesn't fit the steps (a missing field, a type error in a template) | the message names the step, the template and the signature |
| conforms, but the environment's checker fails | the model assumes more of the environment than it gives | the recording misses steps that matter |

Every verdict, its expectation and its claims over many runs are in
[`references/verdicts.md`](references/verdicts.md).

Don't loosen a check to get green. If the model is wrong, change it
deliberately and say why in the commit and the README.

## CI layout

- `go test -short ./...`: scripted scenarios, mutants, `Batched` Hegel
  runs (about a minute).
- `go test ./...`: adds the `FindViolation` shrinking tests.
- A separate job runs the real environment and validates its logs, with
  `-noitf`. Keep it in CI so model drift shows up.
- Install quint and its Rust evaluator in CI (`ci/install-quint.sh`).
  Tests that need quint start with `qtest.RequireQuint(t)`, so they skip
  without it.
