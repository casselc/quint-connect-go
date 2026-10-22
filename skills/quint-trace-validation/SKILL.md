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
| Annotation | `//quint:action action:deposit account:acct amount:n obs.balance:b.bal` on a function, built woven: `qweave go build` (this repository's weaver, an overlay), `go run github.com/DataDog/orchestrion go build`, or in gosim (a `gosim.tool.go` naming qobs) | function-shaped steps, no code change in plain builds |
| Structured logs | `slog.New(qobs.NewSlogHandler(next))`, then `slog.InfoContext(ctx, "deposited", "quint.action", "deposit", "account", a, ...)`: records with `quint.action` are steps, every record still reaches `next` | a service that already logs what it did |
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
    # account, amount: the step fields of those names (the default)
    outcomes: { error: skip }   # a refused call is no model step
    expect: 'balances.get({{.account}}) == {{.obs.balance}}'
invariants: [noOverdraft]
```

Rules:
- A parameter with no template is the step field of its name, rendered as
  a Quint literal. Set, list and map parameters (type aliases included)
  are converted from the recorded JSON by `toSet`, `toList` and `toMap`.
  Give a template only to map a field differently, e.g.
  `args: { o: '{{.outcome}}' }`. An action that needs nothing else can
  be `name: {}`.
- A failed step whose templates ignore `{{.outcome}}` must be mapped in
  the action's `outcomes` (`skip`, or `call` to replay it as a success).
  The tool refuses to guess.
- To also emit a model-level ITF in-process, write argument templates as
  Quint literals (no operator calls). `expect` may be any Quint boolean.

Then lint: `quintgo lint -binding bank.binding.yaml -src ./pkg`. It checks
the binding against `quint typecheck`, the annotations against the
binding, and the literal action of every `qobs.Record` call against the
model (a misspelled action gets a did-you-mean).

In the editor and in CI, the same checks run as analyzers (qanalysis):
- `quintvet ./...` (built from `qanalysis/cmd/quintvet`, or
  `go vet -vettool=$(which quintvet)`) runs the simulation, weaving and
  annotation checks; `-quintaction.binding app.binding.yaml` adds the
  binding's. In golangci-lint, enable the `quintgo` module plugin.
- An annotation's expressions run on return: use parameters, the
  receiver and named results. quintaction names a local it sees.
- `actionmap -coverage summary.json ./...` (the summary of a woven
  coverage run) lists per action what its code can reach but no run ran,
  and what its steps ran that it cannot reach (callers' and setup code).

## 3. Check

- In a test:
  `qtest.Record(t, "bank.binding.yaml", func(*qobs.Recorder) { scenario() }).Expect(t, qtest.Conforms())`.
  Use `qtest.Validate` for steps recorded elsewhere. (`RecordAndCheck`
  and `Check` are deprecated: every verdict is an `Expect`.) Start with
  `qtest.RequireQuint(t)`, which skips the test without quint.
- **Expected verdicts** (mutants): `qtest.Record(...).Expect(t, e...)`,
  where each `e` says how the run must show up:
  - `qtest.Conforms()`;
  - `qtest.Rejects("ack")`: a step the model cannot take;
  - `qtest.Mismatch("read")`: a failed `expect`;
  - `qtest.Violates("prop")`: the run conforms and the invariant fails.

  On a miss, the failure message is the report.
- In code: `validate.NewChecker(binding, opts).Check(ctx, steps)`. Use
  `CheckRuns` for many runs in parallel.
- From files: `quintgo validate -binding b.yaml FILE...`. FILE may be
  OTLP/JSON, a step log or ITF, detected by content. Add
  `-convert rules.yaml` for plain app spans. Exit status: 0 OK,
  1 violation, 2 tool error.
- Converted spans: `quintgo convert -rules bank.convert.yaml spans.jsonl > steps.jsonl`.

### Production logs: select and sanitize first

```sh
QUINTGO_SALT=$SECRET quintgo steps -actors 'checkout-*' -since 2026-10-01T00:00:00Z \
  -pseudonymize account,customer -binding app.binding.yaml -out clean.jsonl spans.jsonl
quintgo validate -noitf -binding app.binding.yaml clean.jsonl
```

- `-binding` keeps only the fields the binding reads, and drops error
  texts: what a check needs, nothing more.
- Pseudonyms keep equality, so interned ids (`intern:`) still work. A
  model constant naming concrete ids (`ACCOUNTS: Set("alice", ...)`) must
  name the pseudonyms instead (`qtrace.Filter.Pseudonym`), or intern the
  field.

## 4. Read the report

| Verdict | Meaning | Usual cause |
|---|---|---|
| `step [i] is not a transition` (disabled, QNT513) | the model cannot take the observed step from the state reached | implementation bug; or a wrong binding (argument mapping, missing restart, wrong outcome) |
| `state does not match what was observed` (QNT508) | the step was possible but `expect` fails; the report shows `expect:`, `obs:` and the model's state after the step | implementation computed a different state, or a wrong `expect`/observation |
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
- **Distributed systems** (`examples/glomers`):
  - Record each node's steps with the node as `thread` and a Lamport
    clock as `order.lamport`, carried in messages. Per-node step logs
    then merge correctly: `quintgo validate n1.jsonl n2.jsonl ...`.
  - Record a shared service's operations where they take effect, at the
    service, not at the client that hears back later.
  - Record collections (sets of values, vectors) as slices or maps.
    They travel as JSON, and `toSet`, `toList` and `toMap` turn them into
    Quint values in templates.
- **Model the environment you run on.** If the code depends on a
  service's guarantees (seq-kv's consistency), the model must not assume
  stronger ones. Check against the real environment too: examples/glomers
  shows two counter designs that passed a too-strong model and failed
  Maelstrom.
