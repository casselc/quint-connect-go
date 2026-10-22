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
- **A2. Mutation testing, woven by qweave.** A mutant per covered block
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
  replayable. One `Run` type for Explore, FindViolation and qgosim waits
  for U4's `qtest.System`, which returns it.
