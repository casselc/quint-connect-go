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
  take `testing.TB`; `Fuzz` takes an `ExploreConfig`. *Done,* but `Sweep`
  keeps `*testing.T` (a subtest per row needs `t.Run`); every in-repo
  caller moved to Record/Validate + Expect or a System.
- **V6. First hour.** `quintgo scaffold -system` prints a runnable System
  test with a mutant claim; `examples/hello` (one service, one retry, one
  dedupe mutant, under 150 lines) is the README's first simulated
  example; examples/bank gains a System test. *Done:* scaffold -system
  (its test compiles, fails until Run records steps, and skips the
  mutant claim until one is injected); examples/hello (80 lines, in CI's
  demos); bank's `TestTellersAsASystem`.
- **V7. Messages, second pass.** A missing step field names the fields
  the step has and the `args:` fix (no Go template text); an unknown
  action says "did you mean" and lists them; QNT508 out of reports; one
  step numbering; failing reports print the instance's constants; the
  strict-recording panic is a failed run, not a dead test binary; "a
  apply" and "do not show the run conforms" reworded. *Done:* reports
  number steps from 1 ("step 2"), the recorded seq and source after the
  step; quint's error line only for errors the report cannot read; a
  failing report's `instance:` line; a missing field names the step's
  fields and the `args:` fix; unknown actions say "did you mean"; a
  strict run's escaped step is logged (`qobs.Escaped`) and fails the
  exploration.
- **V8. Quiet Hegel, and claiming nothing found.** `FindViolation` and
  `Shrink` default to Hegel's quiet verbosity and no reproduction blob
  for an expected failure (`QUINTGO_VERBOSE=1` restores them);
  `Shrink(...).Expect(t, qtest.Conforms())` states that Hegel finds
  nothing, as `FindTrigger(...).Expect(t, false)` does. *Done:* Hegel's
  os.Stdout is captured while it runs (it has no other output) and shown
  only for an unexpected outcome; examples/hello's TestHegelShrinks.
- **V9. Small duplicates, second pass.** One directive parser (a golden
  table binding and qweave both run); `qobs.Attributes` from
  `qtrace.Step.Attributes`; one checker cache and steps key for qtest and
  quinthegel; simdeps lists qmut, qenv/weave and examples/twophase, or
  derives its list from qenv's importers. *Done:* binding splits
  directives as qweave does (it split `f(a, b)` in two), one table both
  test; `qobs.Attributes` is `Step.Attributes` as OTel attributes;
  `validate.Cached` and `qtrace.Key`/`Keyer` serve qtest and quinthegel
  (qtest's cache handed a later test an earlier one's Dir and NoITF);
  simdeps adds every importer of qenv but quinthegel.
- **V10. Package boundaries.** `qobs.OTelSink` to `otelio` (qobs and qenv
  OTel-free: smaller gosim translations); qmeta keeps the chain and
  `Recovery`, `Recovers`, `Assess`, `Of`, `OfSimulation`, `System`,
  `TriggerSpace`, `Runs`, `Count`, `Mostly`, `Simulate` move to qload
  (qmeta independent of qenv again; one `System` name); qenv's choosers
  and byte rule to `qenv/choice`, `Profile` to `qenv/profile`. *Done,*
  but OTelSink stays in qobs: qobs reads a step's thread from the span
  in its context, which needs otel/trace, and that imports
  otel/attribute, so moving the sink would break an API and save
  nothing. The recovery API and `SimulateTrajectory` (was
  `qmeta.Simulate`) are qload's; qmeta imports nothing of the module.
  qenv re-exports choice and profile (aliases), so callers are unchanged.
- **V11. Faults, completed** (needs V10). `FaultPoint` declares a
  `FaultKind` (errors named, `crash` always an option); one registry
  (`DeclareFault` into `Faults`); the doubles' options named
  (`request=duplicate` in reports and scripts); nemeses recorded by name;
  `qenv.NoFaults` and rates set by kind as reports print it
  (`Rates.Set("rpc.x.request", p)`), a zero `Rates` documented as
  `DefaultRates`; crashes per virtual second by default; Script with a
  partial order (per kind, `After` for dependencies). *Done,* but crashes
  per virtual second by default go with R2 (they change every
  exploration's crash frequency, so the tests' rates would need
  retuning, like V12's CrashWindow default). The registry is choice's
  (Declare, DeclareSite); recorded choices carry the option's name, a
  nemesis its own; `Rates.Set`, `NoFaults`; Script's `Follows`.
- **V12. Replay stability** (needs V11). Choice streams carry a format
  version (a corpus header); `qenv.Replay` reports a divergence (step,
  expected kind, got) as an error; then `Service.CrashWindow` on by
  default. *Done:* `Replayer.Err` (Explore reports it as the run's
  error), `EncodeChoices`' version header (`Bytes.Err` on another);
  CrashWindow's default is R2's, as R2 says.
- **V13. Evidence, completed** (P8's rest, P2/P4/P6's leftovers). Names
  the kinds and untried pairs and explains "one way"; `System.Mutate`
  returns Evidence; `Evidence` and `qcover.Summary` as JSON under
  `QUINTGO_KEEP` (what `quintgo cover -sim` and actionmap read); claims
  over chance (`qtest.Reaches(kind).InAtLeast(0.8)`, `NoSurvivors()`
  with marked equivalents); `Recovery.Expect`; `qmeta.Healthy{MaxU}`
  giving the predicate and the PromQL; `Experiment.Healthy` unset an
  error. *Done:* the report names untried and one-way pairs; Mutate's
  report has the baseline (`Evidence()`) and `Expect(t, NoSurvivors(...))`;
  `evidence.json` under QUINTGO_KEEP (the readers are later work);
  `Reaches(kind).Faulted().InAtLeast(p)`; `qload.Recovery.Expect` and
  `Err`; `qmeta.Healthy` and `HealthyRegion`.
- **V14. Lints that see more.** `quintgo lint -sim` includes `_test.go`
  (System `Run` closures live there); quintaction checks literal action
  names in `qobs.Record` against the binding. *Done:* lint -sim scans
  tests; literal Record actions are checked by `quintgo lint -binding
  -src` and quintaction, with a did-you-mean.
- **V15. Smaller fixes from the gotchas.** `FileSink` durable by default
  (the buffered one renamed); a thread used from two goroutines fails at
  record time; `qenv.Wait`/`Signal.Wait` fall back to real waits outside
  a simulation; qweave the default weaver for qenv/weave. *Done:*
  `FileSink` durable (`BufferedFileSink` the old one, `DurableFileSink`
  deprecated); `Recorder.Issues` (a Thread from two goroutines), which
  fails the run; real waits outside a simulation; qweave first in the
  docs (CI already wove edgepublish's sim with it).
- **V16. P7, finished.** quint-metastability, quint-trace-validation and
  quint-hegel split into references; quint-start rows for gosim, weaving,
  Kubernetes controllers and fuzzing, with a ten-line System snippet; the
  `v.Summary` advice removed; a skills test that fails on deprecated
  identifiers. *Done:* the three skills split into references
  (simulation, experiment, alerts; emitting, binding, production-logs,
  patterns; modes, pitfalls); quint-start's new rows and System snippet;
  `v.Summary` advice gone; `TestSkillsAvoidDeprecated`.
- **V17. Models.** Refusals as steps in checkout (DESIGN §26's open item),
  with the model action and the mutants it kills. *Done:* `refuse(order,
  qty)`, enabled when stock < qty; the stock check's two mutants killed,
  51 of 70 (73%).

- **R1. Releasable modules.** A `go.work` for development; no `replace
  => ../` in qanalysis and qgosim; tagged core and sub-modules; `go
  install …/qweave/cmd/qweave@vX` and `…/qanalysis/cmd/quintvet@vX` in
  the skills instead of `go run -C`.
  *Waiting:* tags are outward, so they wait for the maintainer; and a
  `go.work` changes how every module's dependencies resolve (workspace
  MVS takes the highest version any example needs, so the core would
  build against versions its go.mod does not name), so it goes with the
  tags, CI moving to `GOWORK=off` per module.
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

## After the third review (W)

From the third architecture and design reviews (docs/REVIEW.md, "Third
review"). Nothing is released, so compatibility is no constraint: shims,
aliases and deprecated names go. The module rename stays parked; R1's
tags wait for it. Decided with the maintainer: all of it, the global
recorder opt-in.

- **W1. Bugs.** Escaped steps compared by count (the 1000-message cap
  hid every later one); a Scripted kind with the prefix fires; `Rates.Of`
  and Swarm match at dots; a tool error names the first seed, not a map's;
  `Shrink` runs the System's Opts and the exploration's rates; the shrunk
  run's faults and a replay hint in Hegel's log; qload keeps `Sim.Run`'s
  error; `Recovers` judges against the offered load; `quinthegel.Config.Parallel`
  (ignored) gone. Done, but `Shrink` with Opts and rates, which needs W4's
  `System.Rates` and goes with it; `Mostly`'s tie (half recovering counts
  as recovering) is documented, not changed.
- **W2. The global recorder opt-in.** `qtest.Record` and quinthegel install
  it only with `qtest.Global()`; installs restore by identity and a second
  concurrent one fails; with no default global, an escaped step means one. Done
  (`qobs.InstallGlobal`; Record is strict and names `qtest.Global()` when a
  step escapes; `Recorder.Install(global)`).
- **W3. Removals.** V5's deprecations and their last callers; dead or
  internal-only exports (`Sim.Faults`, `Sim.Native`, `Sim.OnSettle`,
  `Sim.Fault`, `qobs.InContext`, `KeepIn`, `ReplayFiles`, `CheckRuns`,
  `GenerateTest`, the free `qtest.Expect`); qenv/choice and qenv/profile
  internal, qenv the API. Done. `Sim.Fault`, `GenerateTest`, `DeclaredFaults`,
  `FaultOptions` and quinthegel's `CaseChecker`/`CheckCase`/`Verdict` are
  unexported; qenv's aliases of the internal choosers carry their own
  doc comments.
- **W4. One way to configure.** qtest's Options only (Config unexported,
  every option documented, `WithOptions` → `Quint`); `System{Binding, Run,
  Opts}`; quinthegel takes `[]qtest.Option`; `ExploreConfig.Coverage` and
  `Verdicts` automatic; `Fuzz(f, rates)`; one way to sweep (the System's
  Run with RowOf). Done: `qtest.Settings`/`Resolve` let quinthegel read the
  options (`Config.Opts`; `Validate`, `Keep`, `Recorder`, `Global`,
  `Constants` gone, as is `quinthegel.Checker`); `Shrink(t, sys, rates)`
  applies the System's Opts at the exploration's rates; verdicts are
  cached for the test binary (`Exploration.Cached`); `qsweep.FaultKind`
  factors are turned off by the sweep. FindViolation keeps `Config`
  (its Mode and rounds are ignored, as documented).
- **W5. One name per concept.** The reviews' tables: `qload.System` →
  `Target`, `qenv.Recorder` → `ChoiceLog`, `qenv.Faults` → `DeclareFaults`,
  `Chain.Expect` → `Mean`, Evidence the one coverage accessor, recovery
  and trigger claims by name, and the rest. Done: also `qload.Config` → `SimConfig`,
  `qenv.Profile` → `TimeProfile`, `DeclareFault` → `DeclareFaultSite`,
  `qenv.Tx` unexported, `qrun.In` → `InContext`, quinthegel's `Recorder`
  → `Case`, `Chooser` → `Choices`, `EnvChooser` → `ChoiceSource`,
  `Replay` → `VaryTraces`, `qtest.Sweep`/`Swept` → `SweepConfig`/
  `SweepResult`, `Evidence.Failing` → `FailingBy`; claims
  `qload.Recovered`/`StaysDegraded`, `SomeTrigger`/`NoTrigger`;
  `QUINTGO_BACKEND` gone (validate read only it, so CI's typescript step
  never reached validate). `qenv.Current` stays: unlike `qrun.Current` it
  is a simulation task's context only, which woven sync needs.
- **W6. The `fault.` prefix out** of everything users type or read; it
  stays in the Chooser protocol. Done: Rates keys, `Without`, `Swarm`, `Reaches`
  and Script kinds are bare, and a prefixed one panics with the fix;
  `NoFaults` is an empty rate map; run lines print
  `rpc.counter.request=duplicate`, not `fault.…=duplicate(2/3)`.
  `Store.CommitFaults` stays a field for now (W7 decides its default with
  CrashWindow's).
- **W7. Crashes per virtual time, CrashWindow on**, by default on every
  runtime; examples retuned, claims not loosened. Done in part, as decided with
  the maintainer: the crash window is on (`Service.NoCrashWindow` closes
  it; grpcledger's `CrashAfterCommit` goes). Crashes stay per scheduling
  step by default: measured, the examples' runs last from 14 ms
  (streams) to 80 s (checkout) of virtual time, so a 100 ms default
  period gave streams no crash chance and hello one, and no one period
  fits. `NemesisEvery` stays opt-in for load runs (qload sets it).
- **W8. First hour and docs.** A quickstart in README; quint-start in the
  skill list, with a glossary and a first-hour path; qweave's install;
  scaffold fixes; report wording; skills corrected; the examples' own
  hand-written assertions as claims; `quintgo validate` writes no ITF by
  default. Extended after the fourth review (below).

## The move, and what the fourth review adds (M, W9, R)

After W7, the architect and design agents reviewed the progress again and
a planner reconciled their findings with this roadmap. Their verdict: the
W and X plans hold. Two things change the order:
- The move to gotogether and the gosim repo comes before W8, so the docs
  are written once, under the new names.
- Release readiness comes before X, because X1, X2 and X8 write paths,
  environment variable names and workflows.

Every commit leaves every module building and its tests green. Order:
M0 → M1 → M2 → M3 → M4 → M5a → M5b → M6 → W9 → R0 → W8 → R1 → R4 →
X. R5 happens when the maintainer says.

- **M0. Hygiene.**
  - Remove the stale agent worktrees and their branches.
  - Summarize W in REVIEW.md, whose third-review tables were left empty.
- **M1. The gosim repo's history.** chucklehead-dev/gosim's main
  fast-forwards from upstream ffd3a61 to the fork's tip (ac6b9b2).
- **M2. The fork, a module of its own.**
  - One scripted top commit sets the module path to
    `github.com/chucklehead-dev/gosim`. It also changes
    `internal/gosimtool`'s `Module`, which is written into translated
    code. The script lives in FORK.md, so a rebase re-runs it.
  - Translation checks the Go version and names the supported range.
  - The MIT license and attribution stay.
  - Minimal CI.
  - Required by pseudo-version until R5.
- **M3. History to chucklehead-dev/gotogether.** Push main only:
  gosim-fork lives in its own repo now. `origin` is repointed.
- **M4. qgosim on the gosim module.**
  - Remove fetch-gosim.sh, its `REF`, the `.gosim-fork` replace, CI's
    fork checkout, and the "fetch first" notes.
  - simdeps fails if anything but qgosim and examples/gosim imports gosim.
- **M5a. The module path.** `github.com/casselc/quint-connect-go` becomes
  `github.com/chucklehead-dev/gotogether`, by script.
- **M5b. Every other name.**
  - The CLI (`cmd/gotogether`).
  - `QUINTGO_*` → `GOTOGETHER_*`.
  - Wire names: `#meta.quintgo`, `"source"`, `quintgo::expect`, the OTel
    tracer name and the golangci plugin. Error prefixes, temp-dir
    prefixes, skills and their symlinks; `skills install` removes old
    `quint-*` dirs.
  - Prose.
  - Kept: `quint.step`/`quint.action`, `//quint:*`, `QuintOutcome`,
    `QUINT_*` variables whose value goes to quint, the quint-connect
    mentions (the Rust crate), and the analyzers' `quint*` names (they
    check `//quint:` directives).
  - The old names join TestSkillsAvoidDeprecated.
- **M6. The old repository.** A final README commit pointing to
  gotogether; the maintainer archives it. Its gosim-fork branch goes once
  M4 is green.
- **W9. API leftovers.**
  - quinthegel:
    - `NewRecorder` → `NewCase`; `Case.Choices()` → `Recorded()`.
    - FindViolation gets a config without the Mode and Rounds it
      ignores.
    - `Case.Add` stops re-teeing (REVIEW S6).
  - Claims:
    - `Expect` is variadic everywhere.
    - `Recovery.Judge` is unexported.
    - `qload.TB` gives way to `testing.TB`.
  - Configuration:
    - One of `System.Opts`/`ExploreConfig.Opts`.
    - `Settings`/`Resolve` become internal.
    - One spelling for installing the global recorder.
    - `qtest.Global` and `qobs.Global` no longer share a name.
    - One of `AllowEmpty`/`SkipIfEmpty`.
  - Rates in one place across Explore, Fuzz and Shrink.
  - `QENV_CHECK` and glomers' `QUINT_STEPS_DIR` → `GOTOGETHER_*`. The
    rule: `QUINT_*` only where the value goes to quint.
  - crossdb's grace check keeps sub-second precision through the kube
    interceptor, or a test pins the boundary.
  - `Store.CommitFaults`' default, which W6 left to W7 and W7 did not
    decide.
  - `Sweep` keeps `*testing.T` (it needs `t.Run`), documented.
- **R0. Trim the docs.** The done sections (A–D, U, Status, V, R3, W1–W7)
  and REVIEW.md move to docs/HISTORY.md. ROADMAP keeps what is open, and
  DESIGN's superseded sections become pointers.
- **W8, extended.**
  - README's Packages table regenerated from `go doc -short`.
  - The hello snippets in README and the start skill copied from
    hello_test.go, with a test that they match.
  - One environment-variable table.
  - Stale strings: `Sweep.Expect`, the free `qtest.Explore`,
    emitting.md's `SetGlobal`.
  - A quickstart first and one install block.
  - The ASCII diagram fixed.
  - Scaffold:
    - It writes to files.
    - It uses a typed `Mutation`.
    - An empty run is a named Explore error.
  - `-noitf` becomes `-itf`.
  - `ShrunkTo(n)` comes here from X8 for hello_test's hand count.
  - The status line reworded.
  - A stale-docs gate: the deprecated-identifier test also reads
    README.md, AGENTS.md and the examples' READMEs.
  - Done when a fresh clone's quickstart passes as written.
- **R1, reworked: modules that can be consumed.**
  - A go.work for development, and no `replace` in published modules:
    `go install …@version` refuses them, and a dependency's replace is
    ignored.
  - A `GOWORK=off` CI job per published module.
  - Skills say `go install …@version`, and golangci's custom-gcl builds
    the plugin.
  - The supported Go range is documented.
  - Examples stay untagged.
- **R4. Readiness.**
  - A root LICENSE (there is none; gosim's MIT needs one beside it).
  - A CHANGELOG.
  - An API review per package and an apidiff baseline.
  - The tag order: gosim, root, qweave, qanalysis, qgosim.
- **R5. Tags**, when the maintainer says.

The X track follows, unchanged except:
- X1's corpus records the gosim module version next to `BytesVersion`.
- X2 adds the readers of `evidence.json`: `cover -sim` and actionmap
  (V13's "later work").
- X5 runs under `GOTOGETHER_CHECK=1`.
- `ShrunkTo` leaves X8.

Dropped on purpose:
- REVIEW S2's embeddable `qenv.Layer`: `qenv.As` and Wrapper cover the
  chain.
- REVIEW P2's `Together` and `quinthegel.Want`: `Violation.Expect`
  superseded them.
- Renaming the CLI's subcommands.
- Rewriting history to drop a5cc809's 15 MB test binary: .git is 26 MB,
  and the docs cite commit hashes.

## What a green suite proves (X)

From a review of the testing regimes by the architect and design agents,
after W. A green suite today proves less than it looks:
- **It is a fixed sample.** CI re-checks the same seeds and Hegel seed
  every run; nothing bounds the search or widens it.
- **It can be vacuous.** No report says which model actions the runs
  took, or which invariants held only because their antecedent never
  did. `Reaches` is used in one test, `NoSurvivors` in none.
- **The models are not run on their own.** No CI step runs `quint run
  --invariant` on the examples' models.
- **The doubles' contracts are trusted.** `TestIsolationAnomalies` shows
  each Store level allows the anomalies it should, not that it allows
  only those (DESIGN §10.7).
- **The real tiers record their own order.** The model checks the order
  the implementation claims (kvstore records under its lock); nothing
  compares it with the real time clients saw, and the reconstruction
  orders concurrent steps by recording order, sound only when they
  commute.
- **No `-race`.** The recorder and sinks, the native runtime and the
  real servers run without it.

The track, in order:

- **X1. Hygiene (S).** `go test -race -short` on the packages that are
  not simulated (qobs, qtrace, validate, qrun, qenv's native tests,
  kvstore, grpcledger, streams' real tiers). A leak check in qenv's
  TestMain from `runtime.Stack` (no goleak: the core takes no new
  dependency). A nightly workflow with fresh seeds (`Seeds: 2000`,
  `-fuzztime 30m`, Hegel unseeded) whose failures become corpus entries
  (`EncodeChoices`).
- **X2. Model coverage and anti-vacuity (S, then M).** Exploration counts
  each run's actions and outcomes and names the model's actions no run
  took: a `model` line in Evidence; `Takes(action)` (the twin of
  `Reaches` for a model action, with `InAtLeast`); `Exercised()`, every
  model action taken and every declared fault kind faulted in some run,
  exceptions named with a reason. A CI step runs `quint run --invariant`
  on every example model; `quint verify` (Apalache) nightly on the small
  ones, outside `go test`. Then vacuity: an invariant's antecedent (from
  a top-level `implies`, or declared in the binding) gets a witness def
  in the rendered module, and the report says which held vacuously in
  every run.
- **X3. Bounded exhaustive schedules (M; after W7).** A stateless,
  delay-bounded DFS chooser (`qenv/internal/choice`): option 0 is the
  oldest task or nominal, so a non-zero schedule choice is a delay and a
  non-zero fault choice a fault, and "≤ k delays, ≤ f faults" falls out
  of the protocol. `System.Exhaust(t, qtest.Bound{Delays, Faults,
  MaxRuns})` returns an Exploration (the same claims and Evidence);
  `Complete()` fails a search MaxRuns cut short, and Evidence says
  "complete: N schedules" or "truncated". The verdict cache makes
  schedules with the same steps cost a Go run, not a quint check. Wide
  value choices (`sched.wait`'s durations) count against the bound or
  stay nominal: document which. No DPOR (it needs every seam's
  footprint). Targets: checkout's `DualWrite` at f=1, hello's dedupe and
  streams' reorder at k≤2.
- **X4. Real-time order (S–M).** Clients record invoke and complete
  times; if A completed before B was invoked, A's step must precede B's
  (`qtrace`, an issue with a fix, no quint). Then, for overlapping
  steps, `Validate(..., qtest.Order(qtest.Linearize))` searches the
  orders their intervals allow, the model as the sequential spec. Targets:
  kvstore's real tier (a read path outside the lock), grpcledger's.
- **X5. The doubles' contracts (M).** Under `QENV_CHECK=1`, Store emits
  read and write sets with commit versions and a checker
  (`qenv/internal/history`, Elle-style) fails on cycles the level forbids
  (G0, G1c, G-single, G2): soundness next to TestIsolationAnomalies'
  completeness. Reusable on a real database tier.
- **X6. Clock faults (M).** Per-process skew and jumps (`clock.skew`,
  `clock.jump`; option 0 none), applied in `qenv.Now` by the task's
  process; profiles and `ToVirtual`/`ToReal` stay global. Targets:
  streams' fencing, crossdb's grace period, jobqueue's leases (moved onto
  qenv).
- **X7. Storage faults and partitions (M).** A `qenv.Disk` double (lost
  writes not synced, a torn last record, rollback on crash), first
  against the toolkit's own `qobs.FileSink`, then jobqueue's journal;
  partitions between named processes in `Service`.
- **X8. Smaller claims and corpora.** `ShrunkTo(n)` for a shrunk run's
  faults (hello's hand count goes); an exploration corpus
  (`ExploreConfig.Corpus`: saved runs replay first, a failing one is
  written with its reproduce line); one example's mutation test ending in
  `NoSurvivors`, and `Reaches`/`Exercised` in checkout's conforming
  tests; `FollowTraces` in the README and quint-start's table; qload
  monotonicity (more capacity never recovers worse) as self-tests.

Deferred: online streaming verification (resuming quint from a checkpoint
is unsound for nondeterministic models; `quintgo validate -follow` over
rolling windows from `init` if wanted), differential runs (the model is
already the common oracle), DPOR.

Order: X1 → X2 (stage 1 and the models' `quint run`) → X3 → X4 → X5 → X6
→ X2 (vacuity) → X7; X8 alongside.
