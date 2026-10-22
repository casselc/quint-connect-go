# Roadmap

What comes after DESIGN §15–16: connecting code coverage to the model,
Go's profiling and tooling, and simkube's lessons. Each item says what it
answers and where it goes. Tracks run in parallel; within a track, items
are in order. The unification track (U) comes from an architecture and
API review, and its items land before the features that would otherwise
add a second way to do the same thing.

## A. Code coverage against the model

The fault coverage of §15.11 says which faults runs reached. These items
say which code they ran, and what the model says about it.

- **A1. Per-step code coverage. Done (DESIGN §17).** qweave instruments
  blocks (`qweave -cover`) for qcover; each block finds its run and task
  through qrun, and the blocks a task runs between its steps are the later
  step's. Go's own `-cover` counters are global to the process and their
  file format is internal, so a run in parallel with others could not be
  told apart.
  - Gives: which blocks each run ran; for each action and outcome, the
    blocks its steps ran; code run by no step of its task; outcomes of an
    action that ran the same code (the model tells them apart, the code
    does not).
  - Next: an uncovered block after a fault point or double call whose kind
    was never faulted (an error path the exploration did not try; needs
    S3's declared kinds), and a static pass in qweave for blocks no
    function reaches (dead, as `deadcode` finds).
- **A2. Mutation testing, woven by qweave. Done (DESIGN §26).** A mutant per covered block
  (a flipped condition, a dropped statement), each explored. A survivor
  in covered code is code the model does not constrain: the sharper
  answer to "what is this code for?".
- **A3. Go fuzzing over the Chooser's entropy.** A fuzz input is a run:
  `f.Fuzz(func(t, data []byte))` with a chooser fed by data, the model as
  oracle, the corpus as regressions. Coverage-guided exploration, which
  seeds and Hegel are not.

## B. Profiling with the simulation and the CTMC

- **B1. pprof labels by role and action.** qrole's annotations (and
  `//quint:action`) also set `pprof.Do` labels: a CPU profile by model
  action and server.
- **B2. Measured service times.** A benchmark's per-action latencies
  become `qenv.Capacity.Time` (an empirical distribution) and the chain's
  μ, with a check of the exponential assumption (coefficient of
  variation).
- **B3. Flight recorder on alert.** `runtime/trace.FlightRecorder`
  snapshots the last seconds when the rule from `Region.Rules` fires
  (qrole has the gauges): the storm arrives with its execution trace.
- **B4. A virtual-time profile.** Task stacks weighted by virtual time
  spent in each seam, in pprof's format: where a storm's latency goes. In
  its own module (pprof's protobuf).

## C. Go tooling

- **C1. Analyzers.** `quintgo lint -sim` and `-weave` as
  `golang.org/x/tools/go/analysis` passes (gopls, golangci-lint), and a
  new one checking `//quint:action` arguments against the binding.
- **C2. `testing/synctest` as a runtime.** Real goroutines and channels
  with fake time, in-process: qenv's native runtime in a bubble.
  Deterministic time, not schedules: for timing behaviour.
- **C3. Coverage of real environments.** Binaries built with `-cover`
  (Maelstrom, kwok, kind runs), merged with `go tool covdata`, paired
  with the same runs' validated steps: A1's report from a real
  environment.
- **C4. `log/slog` as a step source.** A handler recording steps from
  records with a given attribute, like `convert` for spans.
- **C5. Static action → code.** `go/ssa` call graphs of annotated actions,
  compared with A1's dynamic map.

## D. simkube

simkube (MIT, Rust) captures and replays Kubernetes clusters, with no
determinism, faults or oracle: a source of workloads, not a replacement.

- **D1. Trace reader** (msgpack, v3) in an example module: applied and
  deleted objects become timed tasks against kubesim (fleetrollout,
  crossdb), so explorations search faults around a real workload.
- **D2. Trace → `qload.Shape`.** Event and pod-creation rates per tick: a
  measured arrival process for `qmeta.Calibrate`, and production spikes
  as candidate triggers for `FindTrigger`, scalable beyond replay speed.
- **D3. Pod lifetimes.** KWOK Stage configurations for the kwok demos, and
  kubesim status lag drawn from a trace's lifetime index (a chooser kind,
  option 0 nominal).
- **D4. Sanitizing and selecting step logs** before validation, for
  production OTLP (a `quintgo` subcommand or `convert` rules).
- **D5. Scenario lint**: a scenario's dependencies exist before a run.

## U. Unification

From the architecture and API review (docs/REVIEW.md): where the library
has grown two ways of doing one thing, and the lessons of §15–16 turned
into structure. In order, each before the features that need it:

1. **S1 qrun** (done with A1): one lookup of a run's state.
2. **P2 quantified expectations** and `Expect` on Violation and Recovery
   (before A2: "killed" needs `Some`/`None`).
3. **S4 one run record** with choices aligned to steps (before A3, and
   completes A1's alignment).
4. **P1 `qtest.System`** with an error-returning `Run` and a chooser
   option; P3 Check/Validate folded into it.
5. **S2 chooser `Unwrap`** and S3 declared fault kinds with named options
   (before D3).
6. **P5 run-declared constants**, P6 messages, P4 one recovery API.
7. **S5 one hook entry** (before B1), S6 small duplicates (before C1),
   S7 the orbit's definition.
8. **P7 skills restructured**, P8 one evidence report (as A2 and A3 land).

## Order

A1 first, with S1: it joins the fault coverage we have with code coverage,
and A2 and C3 build on its report. Then U2–U4, D1–D2 (they change what
simulations are fed), B1–B2 (they feed the CTMC measured values), A3, and
the rest, each after the U items it depends on.

## Status

- A1: per-step code coverage, done (DESIGN §17). Its report's next
  classifications (an uncovered block after an unfaulted fault point,
  `deadcode`) wait for S3's declared kinds and the static pass in qweave.
- S1: done (qrun).
- U2 (P2): done (DESIGN §18): `Some`, `All`, `None`; `Violation.Expect`.
  `Expect` on `qmeta.Recovery` goes with P4's one recovery API.
- U3 (S4): choices aligned to steps (DESIGN §19): `Choice.Step`, Script
  waiting on the run's recorder, `Recorder.Env`, `Violation.Choices`
  replayable.
- U4 (P1): `qtest.System` (DESIGN §20): Explore, Sweep, FollowTraces,
  `quinthegel.Shrink`; `Run` returns an error; `ExploreConfig.Chooser`.
  Next in it: one `Run` type across explorations, Violations and qgosim;
  P3 (Check vs Validate); `quintgo scaffold` printing a System.
- D1–D2: done (DESIGN §21): `sktrace`, `qload.Piecewise`, retrystorm under
  a production scale-up, fleetrollout under a replayed workload (and the
  model's `scale` for other writers).
- B1–B2: done (DESIGN §22): pprof labels by role; `qmeta.Measure`, with a
  verdict that changes with the service-time shape.
- A3: done (DESIGN §23): `qenv.Bytes`/`FromBytes`/`EncodeChoices`,
  `System.Fuzz`, `VerdictCache`; CI fuzzes checkout for 60 s.
- U5: chooser `Unwrap` and `qenv.As` (DESIGN §24); fault options by name,
  additive (DESIGN §25: `Faults`, `FaultOf`, `Only`, `Scripted.Name`). The
  `fault.` prefix in rates stays until a breaking release.
- A2: done (DESIGN §26): `qweave -mutate`, `qmut`, `System.Mutate`; on
  checkout it found that no step carried the stock after a cancel (fixed
  in the binding), and that refused orders record no step (a model change,
  open).
- U6: done (DESIGN §27): trap messages (constants at step 1, unscripted
  faults, `QUINTGO_SEED`, unhealthy baselines, λ ≥ capacity); factors
  naming their model constants; one `FindTrigger` over `qmeta.System`
  with `Expect`.
- U7: done (DESIGN §28): the chain's v is the hindsight orbit, qrole's the
  waiting part (`Sample.Waiting`, `TestTwoOrbits`); the lint out of qenv
  (`internal/lint`); S5 superseded by B1.
- U8 (P8): one evidence report (DESIGN §29): `Exploration.Evidence`,
  `Swept.Evidence`. Coverage gaps as expectations and a JSON form wait;
  P7 (the skills restructure) is next.
- Choreo (DESIGN §30): examples/twophase, a Choreo model with named
  transitions; no toolkit change needed.
- C1, C5: done (DESIGN §31): qlint, qanalysis (quintvet, golangci plugin,
  quintaction), actionmap comparing static reach with A1's coverage.
- U8 (P7): done (DESIGN §32): `quint-start`, references on demand, one
  verdicts reference.
- B3, B4: done (DESIGN §33): `qmeta.Watch`; `qenv.Profile` (pprof,
  hand-encoded, no dependency).
- C2: done (DESIGN §34): `NewNative(c, nil)` in a synctest bubble, as is.
- C3: done (DESIGN §34): `qcover.ReadGoCover`, `Compare`, `quintgo cover`.
- C4: done (DESIGN §35): `qobs.SlogHandler`.
- D4: done (DESIGN §36): `qtrace.Filter`, `quintgo steps -out`.
- D5, D3: done (DESIGN §37): `sktrace.Lint`; `Lifetimes`, `Quantiles`,
  kubesim `PodTimes`. KWOK Stages from a trace: deferred (no kwok here).
- A1's classifications: done (DESIGN §37): `actionmap` `Dead`,
  `UntriedErrorPaths`.
- Examples beyond the roadmap: examples/grpcledger (gRPC, simgrpc,
  otelgrpc spans; DESIGN §38) and examples/streams (Kafka, Kinesis, a
  simulated log; DESIGN §39). HTTP is examples/kvstore.

## Next: the second unification (V) and a first release (R)

From the second architecture and design review (docs/REVIEW.md, "After
the roadmap"). The first roadmap is done (Status above) with five planned
things left open: P3 (Check vs Validate), S4's one run record, S2's
context out of the chooser, S6's directive parser and step attributes,
P4/P6's `Recovery.Expect` and `qmeta.Healthy`. The examples of DESIGN
§38–40 and a first hour by a reviewer found the rest. In order; each
names what it needs.

- **V0. Verdicts keyed by instance. Done.** `VerdictCache` keyed verdicts
  by steps only, so a sweep's rows with different constants could share
  one; the key now holds the binding, its constants and the invariants.
- **V0'. Claims checked against the model. Done.** An expectation naming
  an action the model lacks or an invariant the runs did not check fails
  at once: `None(Violates("typo"))` held for nothing.
- **V1. Binding template namespaces.** Arguments also under `.args.X`;
  `process`, `outcome`, `obs`, `step` as argument names a lint error with
  the fix (they overwrite a same-named argument). Before any new example.
  *Done:* instead of a lint error, a parameter of a reserved name defaults
  to `.args.X` (the argument), so no binding needs a template for it.
- **V2. Stalls are errors.** `Sim.Run` returns `ErrStuck` with
  `Sim.Tasks()` when tasks other than daemons wait with no deadline;
  waits that may block forever declare it (`qenv.Daemon(ctx)`, the
  doubles' receives). Explore reports it as a run error, not a verdict.
  *Done:* `qenv.Daemon`, `qenv.WaitIdle` (Queue.Receive, simlog's Poll);
  the scheduler only (the native runtime runs in real time).
- **V3. Runs without a simulation: `qrun.Start`.** It registers the
  goroutine; `qtest.Record` and `quinthegel.Recorder` use it instead of
  `qobs.SetGlobal`; `qobs.Strict` becomes a run key; `qenv.New` inherits
  `qrun.Current()`, and `qenv.WithContext` is deprecated (S2's rest).
  *Done, in part:* `qrun.Start`, used by Record and Install alongside the
  global recorder (goroutines a scenario starts cannot be followed);
  `qenv.New` inherits the run. Strict stays process-wide: a step that
  finds no recorder finds no run either, so a run key could never be
  read. WithContext stays: a chooser handed to another goroutine (or to
  gosim) carries its run, which a goroutine registration cannot.
- **V4. One run record** (S4's rest; needs V3). `qtest.Run{Seed, Steps,
  Choices, Faults(), Interrupted, Unscripted, Code, Err, Report}` for
  `Exploration.Runs`, `quinthegel.Violation` (embeds it) and qgosim;
  `qgosim.Scenario` returns `func(qenv.Chooser) error`, so a gosim run is
  a `qtest.System`; `System.Sweep` takes the System's own `Run`, the row in
  the chooser's context. *Done:* `qtest.Run` is every workflow's record
  (`Explored` an alias, `Violation` embeds it); `qgosim.Scenario` is a
  System's Run; `Sweep(t, sw, nil)` runs the System's Run with
  `qtest.RowOf(c)`.
- **V5. One entry point** (P3's rest; needs V4). `qtest.Check`,
  `RecordAndCheck`, `qtest.Explore(binding, …)`, `SweepExplore`
  deprecated for `Validate`/`Record` + `.Expect` and `qtest.System`;
  README §3, quint-metastability, quint-trace-validation and
  instrumentation.md updated. `Explore`, `Sweep`, `FollowTraces`, `Mutate`
  take `testing.TB`; `Fuzz` takes an `ExploreConfig`.
- **V6. First hour.** `quintgo scaffold -system` prints a runnable System
  test with a mutant claim; `examples/hello` (one service, one retry, one
  dedupe mutant, under 150 lines) is the README's first simulated
  example; examples/bank gains a System test.
- **V7. Messages, second pass.** A missing step field names the fields
  the step has and the `args:` fix (no Go template text); an unknown
  action says "did you mean" and lists them; QNT508 out of reports; one
  step numbering; failing reports print the instance's constants; the
  strict-recording panic is a failed run, not a dead test binary; "a
  apply" and "do not show the run conforms" reworded.
- **V8. Quiet Hegel, and claiming nothing found.** `FindViolation` and
  `Shrink` default to Hegel's quiet verbosity and no reproduction blob
  for an expected failure (`QUINTGO_VERBOSE=1` restores them);
  `Shrink(...).Expect(t, qtest.Conforms())` states that Hegel finds
  nothing, as `FindTrigger(...).Expect(t, false)` does.
- **V9. Small duplicates, second pass.** One directive parser (a golden
  table binding and qweave both run); `qobs.Attributes` from
  `qtrace.Step.Attributes`; one checker cache and steps key for qtest and
  quinthegel; simdeps lists qmut, qenv/weave and examples/twophase, or
  derives its list from qenv's importers.
- **V10. Package boundaries.** `qobs.OTelSink` to `otelio` (qobs and qenv
  OTel-free: smaller gosim translations); qmeta keeps the chain and
  `Recovery`, `Recovers`, `Assess`, `Of`, `OfSimulation`, `System`,
  `TriggerSpace`, `Runs`, `Count`, `Mostly`, `Simulate` move to qload
  (qmeta independent of qenv again; one `System` name); qenv's choosers
  and byte rule to `qenv/choice`, `Profile` to `qenv/profile`.
- **V11. Faults, completed** (needs V10). `FaultPoint` declares a
  `FaultKind` (errors named, `crash` always an option); one registry
  (`DeclareFault` into `Faults`); the doubles' options named
  (`request=duplicate` in reports and scripts); nemeses recorded by name;
  `qenv.NoFaults` and rates set by kind as reports print it
  (`Rates.Set("rpc.x.request", p)`), a zero `Rates` documented as
  `DefaultRates`; crashes per virtual second by default; Script with a
  partial order (per kind, `After` for dependencies).
- **V12. Replay stability** (needs V11). Choice streams carry a format
  version (a corpus header); `qenv.Replay` reports a divergence (step,
  expected kind, got) as an error; then `Service.CrashWindow` on by
  default.
- **V13. Evidence, completed** (P8's rest, P2/P4/P6's leftovers). Names
  the kinds and untried pairs and explains "one way"; `System.Mutate`
  returns Evidence; `Evidence` and `qcover.Summary` as JSON under
  `QUINTGO_KEEP` (what `quintgo cover -sim` and actionmap read); claims
  over chance (`qtest.Reaches(kind).InAtLeast(0.8)`, `NoSurvivors()`
  with marked equivalents); `Recovery.Expect`; `qmeta.Healthy{MaxU}`
  giving the predicate and the PromQL; `Experiment.Healthy` unset an
  error.
- **V14. Lints that see more.** `quintgo lint -sim` includes `_test.go`
  (System `Run` closures live there); quintaction checks literal action
  names in `qobs.Record` against the binding. *Done:* lint -sim scans
  tests; literal Record actions are checked by `quintgo lint -binding
  -src` and quintaction, with a did-you-mean.
- **V15. Smaller fixes from the gotchas.** `FileSink` durable by default
  (the buffered one renamed); a thread used from two goroutines fails at
  record time; `qenv.Wait`/`Signal.Wait` fall back to real waits outside
  a simulation; qweave the default weaver for qenv/weave.
- **V16. P7, finished.** quint-metastability, quint-trace-validation and
  quint-hegel split into references; quint-start rows for gosim, weaving,
  Kubernetes controllers and fuzzing, with a ten-line System snippet; the
  `v.Summary` advice removed; a skills test that fails on deprecated
  identifiers.
- **V17. Models.** Refusals as steps in checkout (DESIGN §26's open item),
  with the model action and the mutants it kills.

- **R1. Releasable modules.** A `go.work` for development; no `replace
  => ../` in qanalysis and qgosim; tagged core and sub-modules; `go
  install …/qweave/cmd/qweave@vX` and `…/qanalysis/cmd/quintvet@vX` in
  the skills instead of `go run -C`.
- **R2. The breaking release, with the rename** (needs V1–V15). The
  `fault.` prefix out of user APIs; V12's CrashWindow default; one name
  per concept (`ExploreConfig.Coverage` → `CodeCoverage`,
  `Exploration.Coverage` → `FaultCoverage`, `Sim.Faults(bool)` →
  `SetFaults`, `Chain.Expect` → `Mean`, `qenv.Recorder` → `ChoiceLog`;
  qtest's `Config` and `Option` one); V5's deprecations removed; the
  module renamed (see the review's names).
- **R3. Real environments in CI.** A kwok job running fleetrollout's demo
  (`go install` kwokctl and kwok; no Docker), then D3's KWOK Stages from
  `sktrace.Quantiles` checked there. *Done:* `fleetrollout.KwokStages`,
  `cmd/kwok-stages`, kwok-demo.sh `TRACE=`; in CI the faithful run
  conforms and ignore-observed-generation is caught.

Order: V1–V2 before the next example; V3 → V4 → V5 → V6; V7–V9 alongside;
V10 → V11 → V12; V13–V16 alongside; R1 any time; R2 last; R3 independent.
