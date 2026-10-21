# Design: model-based testing and runtime conformance for Go against Quint

This is the design record of the `quintgo` spike, which started in
casselc/observability (`quintgo/`, against the otel-chdb models) and moved
here as quint-connect-go. Sections 1–8 are the spike's research and
decisions, lightly edited for the new layout (the edgePublish model is
vendored in `examples/edgepublish/model`). Section 9 covers what was added
in the move: quint-connect parity for `connect`, the Hegel integration,
and span conversion.

The spike's verification ran on Go 1.25, Quint 0.32 (`--backend
typescript`) and Orchestrion v1.13.1. The migrated code is tested with
Go 1.26, Quint 0.33 (both backends) and hegel-go v0.9.27. Anything marked
**speculative** or **unverified** was not run.

The ask had two parts:

1. **MBT for Go**, like quint-llm-kit's quint-connect flow for Rust: drive Go
   code from Quint traces and compare states; possibly via comment
   annotations, code generation or Orchestrion.
2. **Runtime conformance à la PObserve**: the running Go program emits the
   model-relevant facts as OpenTelemetry span events, a reconstructor turns
   live telemetry into a model trace, and that trace is checked against the
   Quint model: that each transition is valid, and the invariants.

Two later requirements: the toolkit must be generic (edgePublish is the first
binding, not the only target), and usable as a Go library from tests, e.g.
property-based state-machine tests. A third came after review: running apps
must be able to emit a checkable trace directly, without reconstructing it
from OTel spans, for tests, simulations, deterministic replays and CI. §6b
covers it.

## 1. Prior art

### quint-connect and the kit (Rust)

- **Trace generation.** Quint produces traces: `quint run --mbt` (random
  simulation) or a deterministic `run` test. `--mbt` adds two fields to every
  ITF state: `mbt::actionTaken`, the innermost action that fired, and
  `mbt::nondetPicks`, a record of `Option`s holding each `nondet` value
  picked on the way. (Checked on edgePublish: `writeTable` shows up with
  picks `x`, `o`, and also the step-level picks `w` and `p` that it did not use.)
- **Driver.** `quint-connect` is a Rust crate. `#[quint_test(spec, test, main)]`
  replays a named `run`; `#[quint_run(spec)]` replays random simulations. Both
  call the Quint CLI to get ITF. The user implements a `Driver`:
  `step(&mut self, &Step)` dispatches on the action with the `switch!` macro,
  as in `switch!(step { init => self.init(), MyAction(a, b?) => self.my_action(a, b) })`,
  and a state check compares the spec state (`step.get_in(path)`, deserialised
  with `itf`/serde) with the implementation's, converted with `From`.
  `QUINT_SEED` and `QUINT_VERBOSE` control reproduction.
- **The kit.** `agentic/scripts/quint_connect/project_scaffold.py` runs
  `quint compile --flatten false`, derives Rust types from the spec's type
  declarations, and fills templates (`driver.rs`, `state.rs`,
  `transition.rs`, `types.rs`). The templates assume a Choreo-style spec,
  where the state is `s.system` per process and each transition has a
  `transition.label`. The `mbt-validator` agent then adds one transition per
  TDD cycle: run the test, see "Unimplemented action", add the label, the
  `switch!` arm and a handler that asserts every effect the spec emits, and
  stop. The rule throughout: the spec is ground truth; fix the code, not the
  test. The template pins a private quint-connect branch named
  `connect-and-observe`, a hint that Informal is working on the observation
  direction as well (**speculative**).
- **Quint's docs** list "trace validation" as the inverse of MBT: production
  data, model expectations. They have not written that page yet.

### PObserve (P language, AWS)

A log parser turns each log line into `PObserveEvent{key, timestamp, event,
logLine, customPayload}`. A **sequencer** sorts events by timestamp into one
global stream. A **demultiplexer** routes them by `key` to instances of P
spec monitors, which assert safety (and bounded liveness) over the events
they observe. PObserve runs post hoc, on logs, in test and in production. Its
monitors are hand-written P `spec` machines that watch events; they do not
replay the system model.

What quintgo keeps from it:
- partition keys: `quint.actor`;
- post-hoc checking of real telemetry.

What it changes:
- It replays the model's own transitions, so it checks membership in the
  model and the model's invariants. No separate monitor has to be written.
- Ordering does not trust wall clocks, which is PObserve's weak point:
  see §4.

## 2. Options for Go

| | Annotations + codegen (`go generate`) | **Orchestrion aspects** (`//quint:action`) | Library only (`qobs.Record`) |
|---|---|---|---|
| Code change | comment, plus generated wrappers the code must call, or a rewritten copy of the source | a comment on a function | one call per step |
| Build | extra generate step; generated files to keep in sync | `orchestrion go build` / `go test`; a plain build ignores it | none |
| Off by default | only with build tags | **yes**: without Orchestrion the comment is inert and the binary is unchanged | needs a runtime switch (`qobs.SetGlobal(nil)` makes it about 22 ns a call) |
| Granularity | function | function (statement level is possible, **unverified**, see below) | any statement |
| Timing of the step | wrapper returns | function returns (deferred advice) | wherever the call is |
| Tooling cost | a Go AST rewriter to write and maintain | none (the aspect is 70 lines of YAML/template, and already written) | none |
| Build time | small | cold ~70 s (it weaves the standard library too), warm ~2 s | none |

**Recommendation: Orchestrion for the annotations, with qobs as the library
under it.** The directive is the whole integration. It costs nothing unless
you build with Orchestrion, and it is generic: the aspect ships with qobs, not
with the project. The library path stays open for steps that are not
function-shaped, and for tests that record explicitly. A `go generate`
rewriter would be a less capable Orchestrion that we would have to maintain
ourselves.

What makes it "generated from the model" is not advice generation. The
aspect needs nothing model-specific. The model drives:
- `quintgo scaffold`: a binding skeleton, with every action's signature taken
  from `quint typecheck`;
- `quintgo lint`: the binding against the model (action names, parameter
  names, constants, invariants), and the Go annotations against the binding
  (every field a template uses must be recorded).

Rules the annotation route imposes, learned in this spike:
- **Function granularity.** A model step must be a function whose return is
  the step's linearization point. In the stand-in, the three S3 writes, the
  batch start and the rotation are small functions. In the real
  `publish.go`, `writeParquet` and `seal` already are. The table insert and
  the manifest write are inline in a closure, and the rotation is inside
  `acquire`. Orchestrion's `directive` join point also matches expressions
  inside an annotated statement, so a statement-level `//quint:step` that
  wraps a call expression looks feasible. **Unverified**: not built.
- **What an expression may use.** Advice runs deferred at return, so an
  annotation may use parameters, the receiver and named results, but not
  locals.
- **Record before spawning dependent work.** Recording at return is correct
  only if nothing that depends on the step was started inside the function.
  The stand-in's `rotate` records before `acquire` starts the background
  seal. Otherwise the seal's step could be recorded before the rotation's,
  and the model has no seal of an unrotated generation.

## 3. Telemetry schema (generic)

One span event per model step, named `quint.step`, on a short span
`quint <action>` that is a child of the span in the context. Attributes are
on the event. For the non-argument keys, readers fall back to the span's and
then the resource's attributes, so a deployment can hoist e.g.
`quint.process` into the resource.

| Attribute | Type | Meaning |
|---|---|---|
| `quint.action` | string | model action name (required) |
| `quint.seq` | int | per-recorder sequence number, **dense** (1, 2, 3, …): a hole means a lost step |
| `quint.recorder` | string | id of the recorder, the scope of `quint.seq` |
| `quint.actor` | string | partition key: which model instance the step belongs to (e.g. `edge-1/traces`) |
| `quint.process` | string | incarnation of the actor (e.g. the producer epoch); a change means the process restarted |
| `quint.thread` | string | program-order key: steps with the same thread are totally ordered by seq (default: the qobs thread in ctx, else the parent span id) |
| `quint.outcome` | string | `ok`, `error`, or an error's own classification (`QuintOutcome() string`, e.g. `ambiguous`) |
| `quint.error` | string | error text |
| `quint.arg.<name>` | string, int, bool, double | an observed parameter, in implementation terms (ids, not model values) |
| `quint.obs.<name>` | same | an observed projection of the post-state (e.g. `obs.batches`: what `_sealed.json` said) |
| `quint.order.<domain>` | int | a domain logical clock: steps of one (actor, process) that share the domain are ordered by its value |

Observations stay in implementation terms. The binding holds the
abstraction function (interning, templates), so the same telemetry can be
checked against another model or design without redeploying.

**Sampling.** Step spans must not be sampled out. qobs takes its own
TracerProvider, and the demo uses `AlwaysSample`. With parent-based sampling,
a step under an unsampled request would be lost, so give qobs a provider of
its own. A dropped export, or a BatchSpanProcessor dropping on a full queue,
shows up as a seq gap. The validator reports it as an `error` issue: the
verdict is unsound. **Not built:** carrying steps as OTel log records
instead, which pipelines usually do not sample.

## 4. Reconstruction (`qtrace.Reconstruct`)

Input: an unordered bag of steps from any number of recorders, processes and
actors (OTLP/JSON files, or in-memory SDK spans).

1. **Dedupe** on (recorder, seq): exporters retry and collectors fan out.
2. **Gap detection.** seq is dense per recorder. Any hole is an `error`
   issue for every actor that recorder reported.
3. **Partition by `quint.actor`.** Each actor is replayed as its own model
   instance, like PObserve's key. Actors that share one model instance, such
   as edgePublish's writer and consumer, need the cross-process edges below.
4. **Order** each actor's steps by a topological sort of a happens-before
   graph:
   - hard edges, **program order**: steps with the same `quint.thread`, by seq;
   - hard edges, **domain clocks**: steps with the same
     `quint.order.<d>` in one (actor, process), by value. Example: batch ids
     come from an atomic counter, so their order is the order pushes
     started, even when the `pushStart` steps were recorded in the other
     order. The seq alone cannot be a hard edge: the id allocation and the
     recording are not one atomic moment. Treating seq as a hard edge next
     to the domain clock produces cycles (there is a unit test);
   - **preference** among steps with no hard edge between them: recording
     order, i.e. seq within a recorder and time across recorders, with time
     made monotone per recorder. Such steps were concurrent. Choosing either
     order is sound when their model actions commute, and the model's
     interleaving semantics already requires that of steps on disjoint state
     (e.g. two pushes' writes). If a recording A happens-before a recording
     B in the Go memory model, then seq(A) < seq(B), so causality inside a
     process that passes through a recorder is preserved;
   - a **cycle** (contradictory keys) is reported and broken at the
     earliest step.
5. **Incarnations.** Processes are numbered in order of first appearance.
   A process change becomes the binding's `restart` action (`crash` for
   edgePublish), the step a dead process cannot report. If the steps of two
   incarnations interleave, that is an `overlap` issue: two writers alive at
   once, which the model does not allow. The round trip in §6 caught a driver
   bug this way.
6. **Missing information.**
   - A missing step: a gap, reported.
   - A missing field: the template fails, naming the step.
   - An unobserved variable: the model computes it. Replay is a deterministic
     `run`, so the model fills in every hidden variable. Here that is the
     queue, the in-flight pushes, and the consumer, which stays at init.

Across processes (**not built**): the writer–consumer link would be a hard
edge from `writeManifest` to the `claim` that read that manifest. Carry the
writer's `traceparent` in the manifest JSON and emit a span link. This is
the one ordering fact wall clocks cannot supply.

## 5. Validation (`binding` + `validate`)

The binding generates a Quint module per actor:

```quint
module observed {
  import edgePublish(PAYLOADS = Set(1, 2, 3, 4, 5), MAX_BATCH = 5, MAX_GEN = 2, ..., CRASHES = true).* from "../../model/edgePublish"
  pure def px(...) = ...                       // binding defs
  run conformsTest =
    init
    // [9] seq 10 epoch-A pushStart(batch=4, gen=g20260924T100000, payload=req-1)
    .then(pushStart(2).expect(writer.pushes.contains(px(2, 1, 4, 1, Encoded))))
    // [15] seq 16 epoch-A rotateGen(gen=g20260924T100000)
    .then(rotateGen.expect(writer.rotated.contains({ epoch: 1, gen: 1 })))
    ...
  run sealMatchesManifestsInvTest = init.expect(sealMatchesManifests).then(pushStart(1).expect(sealMatchesManifests))...
}
```

`quint test --max-samples 1 --out-itf` runs it. Each step is on its own line,
so quint's error position maps back to the observed step (the test-time
`LineStep` table). Nested `A.expect(P)` inside `.then(...)` pinpoints the step
precisely. A top-level `.expect` would only point at the whole chain. Verdicts:

- **disabled** (QNT513, or QNT508 "Cannot continue to expect"): the model
  cannot take the observed step from the state it reached. The
  implementation did something the design does not allow.
- **state** (QNT508 in `conformsTest`): the step was possible, but the
  observed projection (`expect`) does not hold. The implementation's choice
  (a batch id, a generation, a seal's count) differs from the model's.
- **invariant**: the trace conforms, but a model invariant fails on it. This
  is a behaviour the design allows and the design's own properties reject.
  It is the model's known bug, happening in the implementation.
- **issues** from reconstruction (gaps, cycles, overlaps) make a pass
  unsound.

A conforming run's ITF has the module prefixes stripped, and
`mbt::actionTaken` and `mbt::observedSeq` added, so the ITF viewer and
trace tools read it.

**Binding conventions**: what a model needs to be checkable this way.
- Actions parameterised by values that can be observed or derived. A value
  the model picks internally, such as the batch id in `pushStart(p)`, is
  checked with `expect` instead.
- Opaque ids need an abstraction function. `intern` renames them 1, 2, … by
  first appearance, per actor or per process, which covers ids that name
  things in creation order.
- One parameterless restart action, if processes can restart.
- A non-`ok` outcome must mean something: `outcomes` maps it to a model value
  used as `{{.outcome}}`, or to `skip` or `call` per action. If a failed step
  hits an action whose templates ignore `.outcome`, generation stops, so a
  failure is never replayed as a success. This rule came from the round
  trip: a failed seal was being replayed as a seal.
- Constants sized from the trace (`set`, `max`, `procs`), because model bounds
  like `MAX_BATCH` are small.

**Limits of the approach**:
- **Hidden nondeterminism.** The writer cannot tell `Fail` from `Ambiguous`,
  and only a test double knows. With a plain error mapped to `Fail`,
  `ambiguous-seal` would replay as a failed manifest write, and the seal
  undercount would be invisible. It is real in S3, missing in the replay.
  - A deterministic `run` cannot branch. The sound treatment is to replay the
    error as `any { writeManifest(x, Fail), writeManifest(x, Ambiguous) }`
    and ask an exhaustive checker whether some resolution matches every
    later observation, and whether all resolutions satisfy the invariants.
  - That is Apalache: a step-indexed encoding with an `obsIdx` variable, the
    TLA+ trace-validation technique of Cirstea, Kuppe, Merz et al.
    **Not built.**
  - The cheaper route is to observe more. An S3 listing or bucket
    notifications become a second recorder whose steps pin the outcome.
- **Soundness is relative to the binding.** The abstraction function is
  trusted. A wrong template can hide a bug or invent one.
- **Checks are per actor, bounded by the trace, post hoc.** Liveness is not
  checked. Long traces need chunking: validate in windows, starting each
  from the ITF state reached by the previous window (**not built**). The
  typescript backend took 4–8 s per trace of ~30 steps, most of it Node
  start-up.

## 6. Capability 1: driving Go from Quint traces (`connect`)

`connect.Generate` runs `quint run --mbt`. `itf` decodes the result into a
navigable `Value` tree, and `connect.Driver{Init, Actions, Skip, Check}`
replays it. This is the Go counterpart of quint-connect's `Driver` and
`switch!`, with a map standing in for the macro.

The per-project part is the handlers and the state check. For edgePublish
(`examples/edgepublish/mbt`):
- **Scheduling through the effect seam.** Every store write blocks in a gate
  until the handler for that model step releases it with the model's
  outcome (`Ok`/`Fail`/`Ambiguous`). That reproduces the model's
  interleaving of concurrent pushes, S3 faults, rotations and background
  seals exactly, in real goroutines. `crash` releases everything in flight
  as `Fail` and starts a new incarnation.
- **State check.** After every step, the store's table rows, Parquet
  objects, manifests and seals (with batch counts) are compared with the
  model's `tableRows`, `parquet`, `manifests` and `seals`, via learned maps
  from implementation generation ids to model generations.
- **Skipped actions.** The consumer's actions have no implementation here.

Verified: 5 random traces of 30 steps replay against the faithful publisher,
and the manifest-first mutant fails on all 5. Random traces from the full
`step` spend most of their steps in `otherInsert` and consumer actions. A
writer-focused step in the model, or `--step` with a projection, would give
better coverage per trace (roadmap).

**Round trip.** `TestRoundTrip`, under `orchestrion go test`, closes the
loop: model trace → Go → recorded steps → `Checker.Check` → the same model.
5/5 validate. On its first run it found two real problems:
- a driver race: the dead incarnation's seal was recorded after the restart,
  which the reconstructor flagged as an overlap;
- the failed-step-replayed-as-success hole in the binding semantics.

## 6b. Emitting checkable traces directly

The OTel path suits production, where steps ride the telemetry pipeline next
to the app's own traces. Tests, simulations, CI and deterministic replays
need neither the SDK nor a collector. There are two additional paths, and
both are verified with the same verdicts as the OTel path.

**Native step log** (`qobs.JSONLSink` / `FileSink`, `qtrace.ReadJSONL`).
- One JSON object per step, whose keys are exactly the OTel attribute keys of
  §3, plus `quint.time_unix_nano`. Decoding reuses `qtrace.FromAttributes`,
  so the two formats cannot drift apart. It is the same data without the
  span wrapper:
  ```json
  {"quint.action":"writeManifest","quint.actor":"edge-1/traces","quint.arg.batch":1,"quint.arg.gen":"g20260924T100000",
   "quint.arg.payload":"req-1","quint.outcome":"ok","quint.process":"epoch-A","quint.recorder":"20147ffb9e1d",
   "quint.seq":2,"quint.thread":"t1","quint.time_unix_nano":1790292680572863711}
  ```
- It keeps implementation terms, so reconstruction (ordering, gaps,
  restarts) and the binding apply exactly as for spans. A step log can be
  re-checked against another binding.
- `load.Sniff` tells OTLP/JSON (`resourceSpans`), a step log
  (`quint.action`) and ITF (`states`) apart by content, so every command
  takes any of them.

**Model-level ITF** (`binding.Plan` → `StepsToITF` / `ObservedITF`; `validate.ValidateITF`).
- The binding is applied in-process: interning, restarts, outcome mapping and
  instance constants. The trace is written in the shape `quint run --mbt`
  writes, per state:
  - `mbt::actionTaken`;
  - `mbt::nondetPicks`: each parameter as `Some(value)`;
  - `quintgo::expect`: the binding's rendered post-state check;
  - `obs::<name>`: the observed projections.
  `#meta.quintgo` names the spec, module, constants, defs and invariants, so
  the file validates with no binding at hand.
- Turning a template into a pick needs a *value*, not an expression. Without
  a Quint evaluator in Go, argument templates must render to Quint literals.
  `itf.ParseQuint` handles records, `Set`/`List`/`Map`, tuples, constructors,
  ints, strings and bools, and rejects operator calls with a clear message.
  The edgePublish binding moved from `px(...)` to literal records for this.
  Its expects still use `px`.
- Validating an ITF replays `.then(action(picks…))`, matching picks to
  parameters by name. After each step it checks `quintgo::expect` and
  equality for **every model variable the state carries**. So the same code
  validates three kinds of trace:
  - a quintgo-emitted ITF (no model variables; expects only);
  - the enriched ITF of an earlier validation (full model state at every
    step);
  - raw `quint run --mbt` output (`-instance currentDesign`: its constants
    are read from the instance module's source, because Quint does not
    re-export an instance's import).
  Tampering with one variable in one state of a `quint run --mbt` trace is
  reported as a state mismatch at that step. (Verified.)
- Picks are matched by name, which works when the model names its `nondet`
  bindings after the action parameters they feed. edgePublish does. That is
  a convention for the `--mbt` direction, not a guarantee of Quint's.

**Symmetry with `connect`.** A validated run's ITF now carries
`mbt::nondetPicks` next to the model's states, so it has the same shape as
`quint run --mbt` output. `connect.Driver` consumes both.
`scenarios.TestScenarios` replays each observed conforming trace
(`happy`, `rotation-race`, `ambiguous-seal`) through the edgePublish MBT
driver, with store state compared after every step. A trace observed in a
run can be replayed deterministically on a new build. Model traces drive
Go; Go traces go back to the model; either can drive Go again.

**Record then check** (`qtest`).
- `RecordAndCheck(t, binding, scenario)` installs a recorder (memory plus a
  step log) as the global one, runs the scenario, writes the model-level
  ITF, validates, and fails the test with the offending step and invariant.
- `Record` returns the result for expected violations; `Check` takes steps
  recorded elsewhere.
- Checkers are cached per binding, so the model is typechecked once per
  test binary.

Costs, measured: the step log costs ~7 µs per step (reflection-based JSON
of a map, synchronous; `FileSink` is buffered), about the same as the OTel
batch path. It avoids the dependency, not the cost. A hand-written encoder
would be the fix if it mattered.

## 7. edgePublish mapping

| Model action | stand-in (`publisher/publish.go`) | real `chdbexporter/publish.go` | observed args | notes |
|---|---|---|---|---|
| `pushStart(p)` | `begin` (acquire + batch id) | `push`: after `acquire` and `nextBatch` | batch, gen, epoch; payload **not available** | order domain `batch` |
| `writeTable(x, o)` | `insertTable` | `s.Insert(g.ts.insert, …)`, inline in `withConn` | outcome ok/error | ambiguity not visible to the writer |
| `writeParquet(x, o)` | `writeParquet` | `p.writeParquet(s, signal, gen, batch, rb)`, already a function | same | annotatable as is |
| `writeManifest(x, o)` | `commit` | `p.writeObject(s, manifestPath…)`, inline; `writeObject` also writes seals | same | needs an extraction, or `thread:`/statement-level |
| `rotateGen` | `rotate` | `acquire`, `st.cur = g` under `mu.Lock` when `old != nil` | gen | record before the seal goroutine starts |
| `sealGen(gk)` | `seal` | `seal(signal, g)`, already a function | gen, `obs.batches` = `g.batches` | `close()` seals without rotating: emit `rotateGen` first |
| `crash` | inferred | inferred from a `producer_epoch` change | none | |
| consumer actions | none | not in the exporter | none | a separate recorder in the consumer (roadmap) |

**Observable vs abstracted**
- Observed: batch ids, generation ids, epochs, write outcomes (as the writer
  sees them), and the seal's batch count.
- Abstracted: generation ids and payload names (interned); epochs (numbered
  by appearance).
- Not observable by the writer:
  - whether a failed write landed;
  - payload identity across retries. The collector's queue does not expose an
    item id to the exporter. The options are a content hash of the pdata
    (CPU cost), an upstream change to exporterhelper, or dropping payload
    identity, which gives up the retry-duplication properties;
  - the queue itself (the model computes it);
  - the consumer.

**Model/implementation gaps the spike exposed**
- The model rotates on its own. `publish.go` rotates only when a push
  arrives, so an idle generation stays open until `Close`. The model is
  more permissive, which is fine for replay. MBT needed a `Tick` on the
  stand-in.
- `close()` seals the current generation without a rotation. The model can
  only seal rotated generations, so `Close` must be recorded as `rotateGen`
  followed by `sealGen`, which needs `MAX_GEN = observed + 1`.
- Configurations without `store_tables` or Parquet skip writes that the
  model always performs. They need a model flag, or a binding that emits the
  skipped writes as no-ops (neither built).
- The model's `nextBatch` starts at 1 per epoch, as do `publish.go`'s
  per-namespace counters. That coincidence lets batch ids pass through
  uninterned.

**Applying it to the real exporter** (not done, per the brief: no changes to
`chdbexporter`), in about five small edits:
1. Extract `insertTable`, `commitManifest` and `rotate` as functions.
2. Annotate those three and the existing `writeParquet` and `seal`,
   with `thread:env.batch` (no ctx is threaded through `push`).
3. Decide on payload identity.
4. Add `orchestrion.tool.go`.
5. Build the collector with `orchestrion go build`.

The stand-in's binding should then apply unchanged.

## 8. Roadmap

1. **Now (this spike):**
   - generic core: schema, recorder, OTLP I/O, reconstructor, binding with
     lint and scaffold, `quint test`-based validator, ITF, connect;
   - Orchestrion aspects;
   - the edgePublish binding;
   - demo scenarios, MBT and round-trip tests;
   - direct emission (§6b): step log, model-level ITF, `ValidateITF`
     (including `quint run --mbt` traces), `qtest`.
2. **Real exporter:** the five edits above, behind the Orchestrion build
   only. Run the collector's own tests woven, and validate their telemetry
   (`Checker` from `go test`). PBT/state-machine tests can feed
   `Checker.Check` directly.
3. **Hidden nondeterminism:** an Apalache mode (step-indexed module; error
   outcomes as `any {…}`), or S3 observation as a second recorder.
4. **Consumer side:**
   - a recorder in the central consumer;
   - `traceparent` in manifests, so each claim is linked to its manifest write;
   - multi-actor merge into one model instance.
5. **Live operation:**
   - a collector processor or connector that tails `quint.step` events;
   - validation in windows, seeded from the last conforming state;
   - alerting on disabled, state or invariant verdicts and on gaps.
   Budget ~7 µs per step (measured) on the hot path. A push is 4 steps,
   against S3 PUTs measured in milliseconds.
6. **Direct emission:** a Quint evaluator for picks (or `quint` evaluating
   the binding's defs), so model-level ITF can take operator calls; and a
   streaming ITF writer for long simulations. Today the ITF is built at the
   end, from buffered steps.
7. **Tooling:**
   - statement-level `//quint:step`;
   - `quintgo lint` also checking annotation expression types against
     `quint typecheck` parameter types;
   - a writer-only `step` in edgePublish for denser MBT traces;
   - the Rust evaluator backend for speed.

## 9. After the move: quint-connect parity, Hegel, conversion

### 9.1 `connect` and quint-connect

`connect` follows the Rust crate's model:
- Quint generates traces with `quint run --mbt` (`Run`) or `quint test`
  (`Test`).
- A `Driver` dispatches on the action taken (`Switch`, standing in for the
  `switch!` macro).
- Nondet picks decode into Go types (`Pick[T]`, `OptPick[T]`, through
  `itf.Decode`, the analogue of serde with the `itf` crate).
- A `StateDriver`'s state is compared with the model's after every step,
  `init` included.

Deliberate differences, each removing a quint-connect gotcha:

| quint-connect | connect |
|---|---|
| one driver instance for every trace; `init` must reset it | `newDriver()` per trace |
| `QUINT_SEED`/`QUINT_VERBOSE` read with `option_env!` (compile time) | read at run time, plus `QUINT_MAX_SAMPLES`, `QUINT_MAX_STEPS`, `QUINT_BACKEND`, `QUINT_BIN` |
| traces read in `read_dir` order | sorted by sequence number |
| state diff only at `QUINT_VERBOSE>=1` | the failing field paths, always (`.Balances[bob]: model 16, implementation 17`), plus the step |
| `quint test` traces fail with "Missing mbt::actionTaken" | the same limit, with an error that says why and what to do (`NondetPath`, or `connect.Run`) |
| `--n-traces` = `--max-samples` | same |

`Step.State` is `Config.StatePath` applied to the variables. Module
qualifiers are stripped from variable names (`m::choreo::s` is `s`), and
a first path element that still carries its qualifier is matched too.
`NondetPath` reads the action and its picks from a sum-type variable, as
quint-connect's `Config.nondet` does.

### 9.2 Hegel (`quinthegel`)

hegel-go (v0.9.x) runs its engine in-process (libhegel, through purego),
with no Python and no server. Its stateful API discovers `Rule*` and
`Invariant*` methods by reflection. Two directions are useful with a
Quint model.

**Hegel drives, the model judges.**
- Hegel's rules call the implementation with drawn inputs, including
  invalid ones. The implementation, or the rule, records model steps.
- At the end of each case the run is validated through the binding.
- In `Each` mode a rejected run fails the test case, so Hegel's shrinker
  minimises the run against the model. This is the only direction that
  finds code accepting what the model forbids: model-driven tests never
  take a disabled step. `examples/bank`'s SelfTransfer mutant passes
  every model trace and is caught here.

Cost: each validation is a `quint test` process (~0.5–1 s, mostly
start-up), and shrinking runs hundreds of candidates. `CaseChecker`
caches verdicts:
- identical runs, keyed by the instance constants and the steps with
  process and thread ids numbered by first appearance;
- prefixes. If the model rejected run R at its k-th step, any run with
  the same constants and the same first k steps fails the same way. A
  conformance failure at a step, and an invariant violation after it,
  depend only on the steps up to there.

- skipped steps. Steps the binding skips (refused calls, with
  `outcomes: { error: skip }`) become no model call. In a single-process
  run they are left out of the keys, so candidates that differ only in
  refused calls share a verdict.

Prefix reuse is restricted to one actor, with no reconstruction issues
and no domain clocks, where the reconstructed order is the recording
order. On `examples/bank`'s SelfTransfer test, the three together cut
quint runs from ~450 to 53 (about 5 min to 40 s), with the same shrunk
result. Constants are part of the key
because bindings may size them from the whole run (`max "amount"`): a
longer run can have larger bounds, which can enable steps.

`After` mode keeps the distinct runs and validates them in parallel at
the end (`validate.Checker.CheckRuns`, one quint process per run, up to
GOMAXPROCS). We tried batching several runs into one `quint test`, but a
Quint module can import a parameterised module with only one set of
constants unqualified, and bindings size constants per run, so batching
would mean rewriting every template's expressions with instance
qualifiers. Parallel processes are simpler and nearly as fast.

**The model drives, Hegel varies.** `Replay` draws a trace index and
optionally a prefix length, then replays the trace on a driver built
from the Hegel test case. Handlers can draw what the model abstracts:
payloads, timings, interleaved invalid requests that must change
nothing. A failure shrinks toward trace 0, a shorter prefix and simpler
draws.

Not built:
- driving the model live from Hegel rules (checking each rule's
  precondition in Quint). It would need a Quint evaluator in-process, or
  a long-lived quint REPL; the post-hoc check gives the same verdicts at
  the end of each case.
- seeding Hegel from Quint traces (Hegel has no explicit-example API in
  Go yet).

### 9.3 Converting existing telemetry (`convert`)

Not every system can be instrumented with model steps. `convert` maps
an application's ordinary spans to steps:
- span name globs, with `where:` attribute filters, or span events;
- each argument, observation, order key and identity is an expression:
  `attr:K` (looked up on the event, then the span, then the resource),
  `span:`, `resource:`, `event:`, `name`, `trace_id`, `status`, a
  `=literal`, or `|int` to convert.

Converted steps are numbered densely per recorder in time order. Native
`quint.step` events in the same input pass through untouched. What
conversion cannot give:
- **gap detection.** A missing span is invisible: there is no dense
  sequence number from the source;
- **program order beyond time order**, unless a `thread:` expression
  (e.g. `trace_id` for strictly sequential traces) supplies it. Concurrent
  operations within a span's resolution are ordered by end time, which is
  sound only where those operations commute in the model.

The binding is unchanged: the same binding checks recorded and converted
steps (`examples/bank`).

### 9.4 Long replays and quint's error positions

A replay is one `init.then(c1).then(c2)...` chain, and the Rust evaluator
rejects chains nested much past 50 steps ("recursion limit exceeded").
`binding.RenderModule` writes longer chains as named segments of 20 calls
(`action conformsTest_seg0 = c1.then(c2)...`), and the run chains the
segments. Each call stays on its own line, so a failure is still located
by line, with two cases quint reports elsewhere:
- the last call of the whole run evaluating to false is QNT511 ("Test …
  returned false") at the run's own line;
- the last call of a segment evaluating to false is QNT513 at the
  top-level `.then(segK)` line.

Both are mapped to the call. A failed test with no location at all is
quint failing, not the model rejecting the run, and is an error. A
400-step concurrent kvstore run validates in about 7 s.

### 9.5 Bulk checking and the Batched mode

Process start-up and model loading cost a quint process ~0.5 s, which
dominates the replay of a short run. `validate.Checker.CheckBatch` works
like this:
1. It reconstructs and plans every run in Go.
2. It groups the actors' traces by their rendered instance constants.
   Traces of one instance can share a module. Constants may depend on
   the run, so grouping, not a union of constants, keeps every verdict
   the one a lone check would give.
3. It writes one module per group and chunk (tests `r<i>_conformsTest`,
   `r<i>_<inv>InvTest`), runs the chunks in parallel, and maps quint's
   output back to each run.
4. A chunk quint cannot evaluate is re-checked one trace at a time.

24 runs: CheckBatch 1.6 s, CheckRuns (a process per run, in parallel)
6.6 s, one at a time ~14 s.

Hegel needs a verdict for each test case before it moves on, and its
shrinker is sequential, so a failing case cannot wait for a batch.
`quinthegel`'s `Batched` mode splits the work instead:
1. **Generate.** `hegel.WithPhases(PhaseGenerate)` and a fixed seed;
   cases are recorded, not checked.
2. **Check in bulk** (`CaseChecker.Prefetch`, which fills the verdict
   cache). If every run passes, start another round with the next seed
   (`Rounds`, `Budget`).
3. **Replay** the failing round with the same seed in `Each` mode. The
   cache decides every case up to the failing one without quint, and
   Hegel shrinks the failure against the model.

If the replay does not meet the failing case (the body was not
deterministic in Hegel's draws), the bulk failure is reported unshrunk.
On a 40-case machine:
- all runs passing: Each 22 s, After 3.3 s, Batched 3.4 s;
- with a bug: Each 32 s (shrunk), After 3.4 s (not shrunk), Batched
  23 s (shrunk, same counterexample).

Batching amortizes start-up over runs that are all new (generation); the
cache reuses verdicts across runs that share prefixes (shrinking).

### 9.6 Distributed systems (`examples/glomers`)

Three things were needed beyond the single-process examples:
- **Cross-process order.** Each node records its steps with itself as
  the thread and a Lamport clock as the domain clock (`order.lamport`);
  the clock rides in message bodies.
  - Reconstruction then orders a delivery after its send, even across
    per-node step logs written by separate processes under real
    Maelstrom.
  - Getting this right needed a fix: steps with equal clock values are
    concurrent, and every one of them must precede every step with a
    larger value. Linking sorted neighbours only left `a(3)` unordered
    with `c(4)` in `a(3) b(3) c(4)`.
- **Linearization points at services.** A KV operation takes effect at
  the service, and the node only hears of it later, after other
  operations may have happened. The simulator's lin-kv records its
  operations itself.
- **Collections as observations.** Gossip carries sets and vectors, and
  reads return them. qobs records slices and maps as JSON text, and the
  binding's `toSet`, `toList` and `toMap` turn them back into Quint
  values.

Long, real runs also needed the replay segments (§9.4) to nest. At 2841
steps, 142 segments chained at the top level were again too deep for
the Rust evaluator, so segments are now grouped into segments
recursively, and no chain is longer than 20.

### 9.7 What a check costs

Measuring a 2841-step Maelstrom log of unique ids (152 s at first) showed
where a replay's time goes:
- **quint's start-up and parse**: ~8 s for a 660 KB generated module;
- **the evaluation itself**: ~5 s;
- **writing the ITF**, the model's full state at every step: 105 MB when
  the state holds every id issued, and as long again to annotate in Go;
- **one more full replay per invariant**: each invariant had its own run
  (for precise attribution), and each wrote its own ITF, which nothing
  read.

Changes:
- **The fused run.** A replay module first holds one run that checks
  each step's expect and every invariant together (`allTest`,
  `binding.FastTests`). It decides a passing run in one replay. Only the
  runs that fail it are replayed again, from a second module
  (`<base>_detail.qnt`, `binding.DetailTests`), whose conformance run and
  per-invariant runs say which check failed and where. Failing runs pay
  a second quint process; passing ones, the common case, save k replays.
- **Optional ITF.** `Options.NoITF` (CLI `-noitf`) skips it, and Hegel's
  checks always skip it, since they only need verdicts.
- **Segment names** no longer contain "Test", so `quint test --match`
  does not take them for tests.

The model matters as much:
- **A history property should not be an invariant that re-scans the
  history.** `issued.keys().forall(i => issued.get(i) == 1)` is
  quadratic over a run. A ghost flag set when a new element repeats an
  old one is constant per step.
- **A model of the mechanism can avoid the history.** Per-incarnation
  counters are state bounded by restarts, not by ids. They are a proof
  of the specification only with a lemma about the encoding, which a
  property test checks (examples/glomers README).

After both: 64 s (19 s without ITF) for the original model, 21 s (11 s)
for the ghost-flag model, 7 s for the scheme model.

## 10. Simulated environments and fault injection (`qenv`)

Maelstrom found the bugs no in-process check found in examples/glomers.
Each time, the in-process environment had been more forgiving than its
contract: a "sequentially consistent" store that was in fact
linearizable. `qenv` makes the environment's contract explicit, and
adversarial within it.

**A fault is a choice the contract allows.** A lost response, a
redelivered message, a read from an older snapshot and a crash between
two statements are all behaviours that the network's, the broker's or the
store's contract permits. A double makes exactly those choices and no
others, so a failure is never "the harness did something impossible". The
remaining question, whether a contract is the one production has, is
answered by running against more than one profile (§10.2).

### 10.1 One decision source

Every choice goes through a `Chooser`: which runnable task goes next, whether
a fault happens here, which pending message is delivered, how long a
status lags. The conventions:
- **Option 0 is nominal**: no fault, the oldest message, the first task.
  A shrinker that lowers values toward 0 removes faults and canonicalises
  the schedule.
- **Kinds.** Kinds starting with `fault.` are faults, with per-kind rates
  (`Rates`, matched by prefix). Any other kind is a schedule choice,
  uniform under a seed.
- **Backends.**
  - `qenv.Hegel(tc)` draws entropy in fixed-size byte chunks: a run makes
    hundreds of choices, and a draw per choice made a shrinking test 20x
    slower.
  - `NewSeeded(seed)` gives random runs, Maelstrom- or Jepsen-style.
  - `Replay(choices)` repeats a recorded run, and panics at the first
    divergence.
  - `Nominal` makes no faults.
  - `Swarm` turns whole fault kinds off per run.

### 10.2 Doubles with contract profiles

| Double | Contract | Faults (choices) |
|---|---|---|
| `Store` | transactions at `Serializable` (optimistic validation of reads, scans and writes), `SnapshotIsolation` (first committer wins, write skew allowed) or `ReadCommitted` (latest committed reads, last writer wins); `GetForUpdate` row locks; a crashed process's transactions abort | the interleaving of statements; a commit applied but reported unknown (`fault.store.commit`) |
| `Queue` | at least once; FIFO only if `Ordered` | lost acknowledgements, redelivery of a crashed consumer's messages, delivery order |
| `Service` (RPC) | at most one response, maybe never | lost or duplicated requests, lost responses (the ambiguous failure) |
| `Process` | tasks die together; no deferred code runs; a restart rebuilds from durable doubles only | crashes at any step (`fault.nemesis`, per step) |
| kubesim (fleetrollout) | generation bumps on spec change; status lags; resourceVersion conflicts | failed requests, applied updates with lost responses, status lag |

Profiles turn "harness quirk or real bug?" into a table. In
examples/checkout, missing row locks are a bug only under read committed,
and a lock without a write only under snapshot isolation. Which row
applies depends on what production runs.

### 10.3 Scheduling

Tasks are goroutines, but only one runs at a time. A task runs until it
reaches a seam (`Yield`, `Wait`, `Sleep`, an operation of a double), where
the scheduler picks the next one. Code between seams is atomic, so a run
is a function of its choices. Virtual time jumps to the next deadline when
nothing can run. `Settle` turns faults off, heals what persists, and runs
the canonical schedule until quiet. Properties conditioned on quiescence
apply after it.

Code under test reaches the environment only through seams: `qenv.DB` and
`Txn`, its own interfaces for brokers and services, `qenv.Go`, `qenv.Now`
and `qenv.Sleep`. Outside a simulation these fall through to real
goroutines and time. Production puts real clients behind the interfaces.

### 10.4 Recording and checking

Steps are recorded where effects happen:
- `Txn.OnCommit` runs at the commit itself, even when the caller is told
  the outcome is unknown. In production, the step goes in a table in the
  same transaction.
- A remote service records its own operations (the payment provider).
- A simulated API server records patches as it applies them.

`qtest.Explore` runs a scenario over many seeds and checks every run in
one `CheckBatch`. `Exploration.Expect` states either that all conform, or
that some failing run shows the expected verdict.
`quinthegel.FindViolation` with `qenv.Hegel` shrinks a failure to the
interleaving and faults it needs. In examples/checkout that took 11 quint
checks; the verdict cache answered the other 599 candidates.

### 10.5 The same model in the real environment

examples/fleetrollout runs one controller two ways:
- in kubesim, under `qenv`;
- on three kwok clusters, with real etcd, kube-apiserver and
  kube-controller-manager, and `kill -9` as the fault.

Both write the same steps, judged by one model. The model works on
evidence (reads ordered by resourceVersion), so it is sound wherever the
controller reads fresh from each API server. Both mutants fail in both
environments.

What the real run needs that the simulation does not: a durable step log
(`DurableFileSink`, because a killed process loses buffered steps and the
loss at the end of a log is undetectable), and recording at the response
rather than at the server. The API server's audit log would be the
faithful recording point.

### 10.6 Running someone else's controller logic (`examples/crossdb`)

crossdb simulates crossplane-runtime's managed reconciler itself, not a
re-implementation of it. Two things made that work:
- **A manager stub.** The reconciler only needs a manager's client and
  scheme, and the client is the intercepted fake.
- **Clock translation at the seam.** The reconciler stamps annotations
  with `time.Now()` and checks them with `time.Since`. Those calls are
  out of qenv's reach. The kube interceptor stores the timestamps in
  virtual time and returns them in real time (`Sim.ToVirtual`,
  `Sim.ToReal`), so a 30 s grace period lasts 30 s of virtual time.

Without the translation the simulation was wrong in a way that looked
like a finding: deletions never finished. With it, the exploration found
real ones:
- a leak with no crash (an ambiguous create, then a delete);
- a leak when the grace period is disabled (deletion during the cloud's
  read-after-create lag).

The same provider, model and audit then ran on kind with full Crossplane,
with steps from two processes merged by a wall-clock domain clock.

### 10.7 Not yet

- **Contract conformance of the doubles.** Each double should be checked
  against a Quint model of its contract, in both directions: everything it
  does is allowed, and everything allowed it can do. Today the store's
  profiles are tested by their anomalies (`TestIsolationAnomalies`).
- **More doubles.** Clocks with skew, disks (lost fsync, torn writes),
  partitions between named processes.
- **Coverage.** Which contract choices and fault combinations a set of
  runs actually exercised, to steer exploration.
- **Go's own scheduler.** Goroutines that bypass `qenv.Go` run
  uncontrolled, and a run that depends on them is not reproducible.
  Replay detects this but cannot prevent it.

## 11. Metastable failures (`qload`, `qmeta`)

A metastable failure leaves no wrong answer to check. The system enters
a degraded state and stays in it after its trigger is gone: a spike
fills a queue, requests time out, and their retries keep the queue full.
No Quint invariant over individual steps says "the system came back".
That is a property of rates over time. Two papers set out how to analyse
it, and this is their pipeline, with each tier checking the next:
- Isaacs et al., "Analyzing Metastable Failures", HotOS '25;
- Alvaro et al., "Formal Analysis of Metastable Failures in Software
  Systems", 2025.

| Tier | Package | Cost | Answers |
|---|---|---|---|
| CTMC over (queue, orbit) | `qmeta.New`, `HittingTimes`, `Field`/`SVG` | ~0.1–1 s per configuration | where the tipping point is; how policies rank |
| calibration | `qmeta.Calibrate` (simulated trajectories) | seconds | the chain's rates, so its numbers mean something |
| simulation | `qload.Simulate`, `qmeta.Recovers` | ~0.1–0.5 s per run of 20 simulated minutes | does this policy, this code, recover from this trigger |
| trigger search | `qmeta.FindTrigger` (Hegel) | ~1 min | the smallest trigger that breaks it, shrunk |
| the real system | (load tests) | hours | confirms the one trigger the search found |

### 11.1 Recovery as the property

`qmeta.Recovers` takes the papers' definition. Within W after the
stressor ends, goodput is back to at least 90% of what it was before. A
trigger is extra load, a slowdown (a capacity dip in a dependency) and a
duration. All three shrink toward none, so Hegel's result is the smallest
stressor that tips the system. A run's escape from the metastable region
is chance, so each candidate trigger runs several seeds and a majority
decides (`RecoversMostly`). Per-request randomness comes from a seeded
generator, not the Chooser: a run has tens of thousands of requests, and
what the search should vary is the trigger.

### 11.2 The code under test on virtual time

qload runs on qenv. That is what lets the retry library itself sit in the
loop (`ClientConfig.Retrier`), rather than a model of it. The library
receives the virtual time as an argument, and limiters such as
`x/time/rate` already accept one. Clients are open-loop (Poisson
arrivals) or closed-loop (`Population`, `Think`). The difference
decides results. A controller's objects are a closed loop: the workqueue
merges a failing object's poll into its retry. The open-loop version of
the Crossplane provider found storms under crossplane-runtime's defaults
that cannot happen. The closed-loop version found none.

### 11.3 What the tiers taught each other

- **Rejection semantics.** In examples/retrystorm the simulation showed
  that backoff alone recovers worse than immediate retries. An immediate
  retry rejected by a full queue spends its retries at once and gives up,
  which sheds load. A retry after backoff comes back. The first chain
  dropped rejected transitions and predicted the opposite. It now has
  both rules.
- **Truncation.** With the orbit bounded at 60, the chain predicted quick
  recovery for backoff. At 120 it predicted a storm. A finite chain must
  be big enough to hold the storm it is asked about.
- **Absolute times.** The chain's recovery times were orders of magnitude
  above the simulation's (immediate retries at 9.5 req/s from (100, 30):
  minutes simulated against days predicted). It ranks policies and places
  the tipping point; for numbers it must be calibrated.
- **A simulator bug.** A `qenv.Signal` wait that timed out stayed in the
  signal's waiter list, so a later `Notify` cut short the task's next
  wait, a backoff sleep. Requests in the orbit outnumbered the controller's
  objects, which is impossible, and that exposed it. The fix tags each
  wait with its generation (`TestSignalTimeoutIsForgotten`). Checking a
  simulation's own invariants (population ≥ orbit) is cheap. Do it.

### 11.4 Correctness stays with Quint

A mitigation is code. The retry budget's accounting is recorded and
checked against `model/retry.qnt`, whose invariant is the amplification
bound. Its `Uncapped` mutant passed the load tier: after 6000 s of quiet
the overfilled bucket still let 4 of 5 runs recover. The model rejected
its first wrong step. The tiers are complementary: Quint for what each
step does, qmeta and qload for what the rates do.

### 11.5 Not yet

- **Multi-server chains.** The chain has one server. A dependency chain,
  where retries amplify at every hop, needs a product of chains or
  simulation only.
- **Calibration of mitigations.** `Calibrate` fits λ and τ. A budget's
  or backoff's effective parameters could be fitted too.
- **Real-system confirmation.** Run the trigger Hegel found against the
  real provider on kind, with a throttling dbaas server (`dbaas-server
  -throttle-rps`).
