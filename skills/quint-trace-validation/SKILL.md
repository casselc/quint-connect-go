---
name: quint-trace-validation
description: Instrument a Go application so its behaviour can be checked against a Quint model, and check it (qobs.Record, //quint:action with Orchestrion, OTel span events, native step logs, converting existing OTel spans with convert rules, binding YAML, quintgo lint/validate, qtest). Use when asked to check production or test runs against a spec, to add model-step telemetry, to write or fix a binding, or to interpret a quintgo validate report.
---

# Trace validation: the running code reports, the model judges

The code emits one **step** per model action it performs. quint-connect-go
then works through the following stages:

1. **Reconstruct.** It orders the steps per actor, using dense sequence
   numbers, program-order threads and domain clocks. It detects lost steps
   and restarts.
2. **Map.** A **binding** maps each step to a Quint call.
3. **Replay.** It generates a Quint module and replays the run with
   `quint test`.
4. **Report.** For each step it says whether the step is a transition of
   the model, whether the observed state matches, and whether the
   invariants hold.

## 1. Emit steps (pick one or more)

| Way | Code | When |
|---|---|---|
| Explicit | `qobs.Record(ctx, "deposit", err, "account", a, "amount", n, "obs.balance", bal)` | anywhere; ~20 ns when no recorder is installed |
| Annotation | `//quint:action action:deposit account:acct amount:n obs.balance:b.bal` on a function, built with `go run github.com/DataDog/orchestrion go build` | function-shaped steps, no code change in plain builds |
| Existing OTel spans | a `convert` rule file mapping span names and attributes to actions | code you cannot change, or telemetry you already have |

Keys in `Record` and annotations:
- `actor`, `process`, `thread`, `outcome` override the step's identity;
- `when:ok|error`: record only on success or only on failure;
- `obs.<name>`: an observation of the state after the step;
- `order.<domain>`: a logical clock;
- anything else is an argument.

The final `error` is the outcome. An error can classify itself with a
`QuintOutcome() string` method (e.g. `"ambiguous"`).

**Record at the moment the step takes effect**, while holding the lock that
orders it, and before spawning work that depends on it.

Sinks (`qobs.NewRecorder(sink)`, installed with `qobs.SetGlobal`):
- `MemorySink` for tests;
- `FileSink("steps.jsonl")` for a native step log;
- `NewOTelSink(tp)` for span events. Use a TracerProvider that never
  samples them out: a dropped step is a reported gap;
- `Tee{...}` to send steps to several sinks.

## 2. Write the binding

```sh
quintgo scaffold -spec model/bank.qnt -module bank > bank.binding.yaml
```

```yaml
spec: model/bank.qnt
module: bank
constants: { ACCOUNTS: 'Set("alice", "bob")', MAX_AMOUNT: '{{ atLeast 5 (max "amount") }}' }
intern: { gen: process }        # opaque ids -> 1, 2, ... by first appearance
outcomes: { ok: Ok, error: Fail }
restart: crash                  # inserted where the process identity changes
actions:
  deposit:
    args: { account: '{{.account}}', amount: '{{.amount}}' }   # rendered as Quint literals
    outcomes: { error: skip }   # a refused call is no model step
    expect: 'balances.get({{.account}}) == {{.obs.balance}}'
invariants: [noOverdraft]
```

Rules:
- Every parameter needs a template.
- A failed step whose templates ignore `{{.outcome}}` must be mapped in
  the action's `outcomes` (`skip`, or `call` to replay it as a success).
  The tool refuses to guess.
- To also emit a model-level ITF in-process, write argument templates as
  Quint literals (no operator calls). `expect` may be any Quint boolean.

Then lint: `quintgo lint -binding bank.binding.yaml -src ./pkg`. It checks
the binding against `quint typecheck` and the annotations against the
binding.

## 3. Check

- In a test:
  `qtest.RecordAndCheck(t, "bank.binding.yaml", func(*qobs.Recorder) { scenario() })`.
  Use `qtest.Record` to inspect expected violations, and `qtest.Check` for
  steps recorded elsewhere.
- In code: `validate.NewChecker(binding, opts).Check(ctx, steps)`. Use
  `CheckRuns` for many runs in parallel.
- From files: `quintgo validate -binding b.yaml FILE...`. FILE may be
  OTLP/JSON, a step log or ITF, detected by content. Add
  `-convert rules.yaml` for plain app spans. Exit status: 0 OK,
  1 violation, 2 tool error.
- Converted spans: `quintgo convert -rules bank.convert.yaml spans.jsonl > steps.jsonl`.

## 4. Read the report

| Verdict | Meaning | Usual cause |
|---|---|---|
| `step [i] is not a transition` (disabled, QNT513) | the model cannot take the observed step from the state reached | implementation bug; or a wrong binding (argument mapping, missing restart, wrong outcome) |
| `state does not match what was observed` (QNT508) | the step was possible but `expect` fails; the report shows `expect:` and `obs:` | implementation computed a different state, or a wrong `expect`/observation |
| `invariant X: FAIL, violated after step [i]` | the run is a behaviour of the model, and the model's own property fails on it | a **design** bug the model already allows, now seen in real code |
| `issue: error gap` | sequence numbers are missing: steps were lost or sampled | fix the pipeline; the pass is unsound |
| `issue: warn overlap` / `cycle` | two incarnations interleave / contradictory order keys | instrumentation identity or order keys are wrong |

The generated module (`module:` in the report) is a plain Quint file:
`quint test` it, edit it, or keep it as a regression test. The `itf:` file
of a conforming run carries the model's full state and `mbt::nondetPicks`,
so `connect.ReplayFiles` can drive the implementation with it again.

Soundness is relative to the binding: a wrong template can hide or invent
a bug. When a verdict surprises you, check the binding first.

## Patterns from the example apps

- **Concurrent services** (`examples/kvstore`). Record each step inside the
  critical section that orders it. For spans, stamp a linearization index
  (`kv.op`) and make it an ordering key in the convert rules
  (`order: { op: attr:kv.op }`). Otherwise concurrent requests are
  ordered by span end time, which is not the order their effects took.
- **Restarts** (`examples/jobqueue`). Give every incarnation its own
  `process`, and let the binding's `restart:` insert the model's restart.
  Make operations that race a restart retry on the new incarnation, so
  nothing is recorded under a dead one.
- **Properties the design does not promise.** Leave them out of the
  binding's `invariants` and check them on demand:
  `quintgo validate -invariants exactlyOnce`, or
  `Checker.WithInvariants`. "Conforms, invariant FAIL" then says when
  and where a real run hit the design's limit.
