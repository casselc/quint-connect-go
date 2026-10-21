# Instrumenting Go code for trace validation

How a Go program reports the model steps it performs, the telemetry
schema those steps travel in, and how a recorded run is checked. For the
reasons behind it, see [DESIGN.md](DESIGN.md) §2–§6b.

## The telemetry schema

A model step is one `quint.step` span event, on a short span
`quint <action>` that is a child of the span in the context. In a native
step log it is one JSON object with the same keys:

| Attribute | Type | Meaning |
|---|---|---|
| `quint.action` | string | model action name (required) |
| `quint.seq` | int | per-recorder sequence number, dense (1, 2, 3, …): a hole means a lost step |
| `quint.recorder` | string | the recorder's id: the scope of `quint.seq` |
| `quint.actor` | string | partition key: which model instance the step belongs to |
| `quint.process` | string | incarnation of the actor; a change means a restart |
| `quint.thread` | string | program-order key: steps with the same thread are ordered by seq |
| `quint.outcome` | string | `ok`, `error`, or an error's own classification (`QuintOutcome() string`) |
| `quint.error` | string | error text |
| `quint.arg.<name>` | string, int, bool, double | an observed parameter, in implementation terms |
| `quint.obs.<name>` | same | an observed projection of the state after the step |
| `quint.order.<domain>` | int | a domain logical clock |
| `quint.time_unix_nano` | int | step log only: the time |

Observations stay in implementation terms. The binding holds the
abstraction function, so the same telemetry can be checked against another
model or design later.

## Three ways to emit, one way to convert

```go
// Explicit, anywhere (a no-op costing ~20 ns until a recorder is installed):
qobs.Record(ctx, "withdraw", err, "account", acct, "amount", n, "obs.balance", bal)

// Install a recorder at start-up, choosing where the steps go:
qobs.SetGlobal(qobs.NewRecorder(qobs.NewOTelSink(tp), qobs.WithActor("bank-1")))   // span events
sink, _ := qobs.FileSink("steps.jsonl"); qobs.SetGlobal(qobs.NewRecorder(sink))    // step log
```

With Orchestrion, an annotation on a function records a step when the
function returns (see "The annotation" below). Spans the application
already emits can be converted instead, with a rule file (package
`convert`, `quintgo convert`, `quintgo validate -convert`):

```yaml
rules:
  - span: Bank/Deposit
    action: deposit
    args: { account: attr:bank.account, amount: attr:bank.amount }
    obs: { balance: attr:bank.balance }
```

## Which trace path when

| | OTel spans | Native step log | Model-level ITF |
|---|---|---|---|
| What the app writes | `quint.step` span events (`qobs.OTelSink`) | one JSON object per step, the same keys (`qobs.JSONLSink` / `FileSink`) | the binding applied in-process: `mbt::actionTaken`, `mbt::nondetPicks`, `quintgo::expect`, `obs::*` (`binding.StepsToITF`, `demo -itf`, `qtest`) |
| Needs | an OTel SDK and pipeline; spans never sampled out | a writable file or `io.Writer` | the binding file, and argument templates that render to Quint literals |
| Terms | implementation (ids, errors) | implementation | model (interned ids, model values) |
| Check with | `quintgo validate -binding b.yaml spans.jsonl` | `quintgo validate -binding b.yaml steps.jsonl` | `quintgo validate trace.itf.json` (the trace names its model; no binding) |
| Rebind later? | yes: same telemetry, another binding or design | yes | no: the abstraction is baked in |
| Use for | production and staging: live telemetry, cross-process, alongside the app's own traces | tests, simulations, CI, deterministic replays, anything without OTel | handing traces to Quint tooling (the ITF viewer, trace tools); comparing with `quint run --mbt` traces; archiving a checked trace; driving the implementation again through `connect` |

All three give the same verdicts on examples/edgepublish's five scenarios, at the same step
(`examples/edgepublish/scenarios.TestScenarios` checks this). A validated run also writes an ITF
with the model's full state at every step and `mbt::nondetPicks`, i.e. the
shape of `quint run --mbt`. `connect.Replay` (or `connect.ReplayFiles`) replays such a trace on the
implementation: `scenarios.TestScenarios` replays each observed conforming
trace through the MBT driver. `validate.ValidateITF` also takes plain `quint
run --mbt` output (`quintgo validate -spec M.qnt -module M -instance
currentDesign trace.itf.json`) and compares every model variable in the
trace with the model's state after each step.

## The annotation

```go
//quint:action action:writeTable actor:p.actor() process:p.epoch payload:b.payload batch:b.id gen:b.gen.id
func (p *Publisher) insertTable(ctx context.Context, b *batch, rows []byte) error { ... }
```

- Each `key:<Go expression>` is evaluated when the function returns. It may
  use the parameters, the receiver and named results (not locals). Quote
  values with spaces: `key:"f(a, b)"`.
- The function's final `error` result is the outcome. An error can classify
  itself with `QuintOutcome() string`, as `ambiguous` in the demo store does.
- Special keys:
  - `when:ok`/`when:error`: record only on success or failure; the
    expressions are not evaluated otherwise;
  - `actor`, `process`, `thread`, `outcome`: override the step's identity;
  - `obs.<name>`: an observed state projection;
  - `order.<domain>`: a domain clock.
- `//quint:thread` on a function with a `context.Context` parameter puts the
  steps recorded under that context in program order.
- A plain `go build` ignores both directives.

## Applying this to a new project

Per project:
- the annotations;
- one binding YAML;
- for MBT only, the driver handlers.

Everything else is generic.

1. **Model conventions.** A parameterised module; actions whose parameters
   can be observed or derived; one parameterless restart action if
   processes can restart; invariants as `val`s. Values the model picks
   itself are checked with `expect`, not passed as arguments.
2. **Scaffold the binding:**
   `quintgo scaffold -spec model.qnt -module M > binding.yaml`. It lists every
   constant, and every action with its parameter names and types.
3. **Fill it in:**
   - `constants`: Quint expressions, which may use `set "arg"`, `max "arg"`,
     `procs`, `add`, `atLeast`;
   - `intern`: opaque ids, renamed 1, 2, …, scoped per actor or per process;
   - `outcomes`: `ok`/`error`/custom mapped to Quint values; per action,
     `skip` or `call`;
   - `restart`;
   - `defs`: Quint helpers;
   - per action, `args` templates over the recorded fields (`{{.batch}}`,
     `{{.process}}`, `{{.outcome}}`, `{{.obs.x}}`) and an optional `expect`;
   - `invariants`.
4. **Annotate** one function per model step. If a step is inline, extract it
   into a function, making its return the moment the step takes effect. Add
   `orchestrion.tool.go` importing `github.com/DataDog/orchestrion` and
   `github.com/casselc/quint-connect-go/qobs`.
5. **Lint:** `quintgo lint -binding binding.yaml -src ./your/pkg`. This checks
   the binding against `quint typecheck`'s view of the model, and that every
   field a template uses is recorded by the annotation.
6. **Emit.** Build with `orchestrion go build`, then install a recorder at
   startup. Pick the sink by where the trace should go (see "Which trace
   path when"):
   - `qobs.NewOTelSink(tp)`, on a TracerProvider that never samples these
     spans out;
   - `qobs.FileSink("steps.jsonl")` for a native step log;
   - in tests, `qtest.RecordAndCheck`, which also writes the model-level ITF.

   For ITF emitted in-process, write argument templates as Quint literals:
   records, `Set(...)` and constructors, not calls of model operators.
   `expect` may use any Quint.
7. **Validate.** Run `quintgo validate [-binding binding.yaml] FILE...`, where
   FILE is detected by content. In tests, use `validate.Checker`,
   `validate.ValidateITF` or `qtest`.
8. **Optional MBT:** write a `connect.Driver` (a `connect.Switch` of handlers), and put a gate at the
   effect seam so the model can schedule the implementation.

## Checking

| | |
|---|---|
| `qtest.RecordAndCheck(t, binding, scenario)` | record a scenario in a test and check it; `qtest.Record` returns the result, `qtest.Check` takes steps; `.Expect(t, qtest.Mismatch("deposit"))` states how a mutant must show up |
| `validate.NewChecker(binding, opts)`, `.Check(ctx, steps)`, `.CheckRuns(ctx, runs, n)` | the library entry points |
| `validate.ValidateITF(ctx, trace, ITFOptions{...})` | a model-level ITF, including raw `quint run --mbt` output |
| `quintgo validate [-binding b.yaml] [-convert r.yaml] FILE...` | from files (OTLP/JSON, step log or ITF, detected by content); exit 0 conforms, 1 violation, 2 error |
| `quintgo steps FILE...`, `quintgo itf -binding b.yaml -out DIR FILE...` | look at the reconstructed order; write model-level ITF |

## Limitations (details in [DESIGN.md](DESIGN.md) §5)

- Hidden nondeterminism is not handled. A writer cannot tell a failed write
  from an ambiguous one; only the test double can. An Apalache mode, or S3
  observation, is roadmap.
- Payload identity across retries is not available in the real exporter.
- Validation is per actor, post hoc and bounded by trace length. There is no
  liveness checking and no windowing yet.
- Two limits of the model-level ITF:
  - its argument templates must render to Quint literals, so operator calls
    cannot be arguments there (they still work on the other paths);
  - it names its model by absolute path in `#meta.quintgo.spec`. Pass
    `-spec` to validate it on another machine.
- `-instance` expects an instance module shaped as a single
  `import M(...).*`; its constants are read from the source text.
- Measured cost per recorded step (4 vCPU VM, `go test ./otelio -bench .`):
  - recording off: ~22 ns (argument boxing);
  - MemorySink: ~1.5–2.9 µs;
  - OTel batch span processor to OTLP/JSON: ~7.1–8.9 µs, 37 allocations;
  - native step log (`JSONLSink`): ~7–7.5 µs, 39 allocations (reflection-based JSON).
  Orchestrion's cold build takes ~70 s. Validation with the typescript
  backend takes 4–8 s per trace.
