# Architecture and API review

Two reviews of the library as of DESIGN §16, one of its structure and one
of what users meet, merged and ranked. The roadmap's U track
(ROADMAP.md) orders them. Each item: what, where, the proposal, and
whether it breaks the public API.

## What is fine as it is

- The module split: core with OTel, yaml.v3 and hegel only; qweave and
  qgosim in modules of their own. The simdeps rule and the Hegel adapters
  gathered in quinthegel.
- The layering of qtrace, qobs, otelio, convert and validate, and sinks as
  the one way to compose recording.
- One Chooser for every source of choice, with option 0 nominal: Seeded,
  Hegel, Replay and Script are interchangeable. The library's best idea.
- The verdict vocabulary (`Conforms`, `Rejects`, `Mismatch`, `Violates`,
  `AnyOf`) and the mutants table as the shape of a test
  (examples/checkout/checkout_test.go `TestMutants`).
- `qgosim.Scenario` plugging into `qtest.Explore`: one scenario, many
  runtimes.
- qsweep without dependencies, qmeta independent of qenv, qload's
  arrivals from its own seeded stream (a trigger shrinks by its
  parameters, not its arrivals), and the doubles inside qenv.

## Structure

**S1. One way to find a run's state (qrun). Done.** qobs (`WithRecorder`
plus a goroutine fallback), qrole (`WithRoles`, no fallback) and the new
qcover each had their own lookup. qrole's woven calls in functions with no
context passed `context.TODO()` and counted into the shared
`qrole.Default`, mixing runs in parallel. `qrun.Key[T]`, `qrun.Get`
(context, then the goroutine's run) and one resolver installed by qenv
replace them; `qobs.SetFallback` is gone. pprof labels (B1) will use it.

**S2. Chooser wrappers: `Unwrap` and one walker.** `EntropyOf` and
`nemesisOf` switch on private types, `ContextOf` sees only the outermost
chooser, and `swarm.keep()` re-hoists the context by hand. A user's
wrapper, or `Script{Base: c}`, loses the context, the entropy (gosim 9×
slower) and nemesis-by-name. Proposal: `Wrapper{ Unwrap() Chooser }`,
`qenv.As[T](c)`, a Recorder that implements Entropy and ChooseNemesis
itself, and an embeddable `qenv.Layer`. With S1 the context can leave the
chooser (deprecate `WithContext`). Not breaking.

**S3. Declared fault kinds with named options.** Options are indices:
`kube.request` has 2 on Get and 3 on Update, so coverage's
`Injected[2]` mixes meanings and a `Scripted{Option: 2}` panics on a Get.
Kinds are spelled three ways ("store.commit", "fault.store.commit",
"nemesis.crash.p"). Proposal: `qenv.FaultKind("kube.request",
"unavailable", "lostResponse")`, `FaultOf` returning the option's name,
named options in Script, connect.Fault and coverage, declared kinds in
`Unreached`, and one spelling without "fault." in every user API.
Breaks `Scripted.Option` (int to string); `Fault(kind, n)` stays as a
shim.

**S4. One run record.** `qtest.Explored` and `quinthegel.Violation` differ;
a shrunk Hegel failure has no choices, so it cannot be replayed, run on
gosim, or kept as a fuzz seed. Choices and steps are separate sequences
(Script's `Progress` is wired by hand). Proposal: `Choice.Step` (steps
recorded before it), one `Run{Steps, Choices, Code, Interrupted, Report}`
from Explore, FindViolation and qgosim, and coverage as reducers over
`[]*Run`. Additive.

**S5. One entry for annotation hooks.** qrole's three aspects and
qenv/fault repeat the same template prelude. Proposal:
`qsite.Enter(ctx, "serve", args) func(error)` with observers (role counts,
pprof labels, an action's `begin`). Before B1.

**S6. Small duplicates.**
- Lint in qenv pulls go/ast into every gosim translation: move it to
  `internal/lint`, with one `Finding` type shared with the binding lint.
- Two directive parsers (binding's and qweave's) split `amount:f(a, b)`
  differently: one exported parser in core.
- `qobs.Attributes` re-implements the step schema.
- simdeps' package list is kept by hand: derive it.
- `qload`'s counter sink duplicates `qrole.MetricsSink`.
- `quinthegel.Recorder.Add` re-tees.
- `Explored.Faults` duplicates `Recorder.Faults`.

**S7. Two definitions of the orbit.** `qload.Sample.Orbit` (in hindsight:
attempts that will time out, and those waiting) and `qrole.Orbit`
(operations minus calls in flight) both calibrate the chain. Say which v
the chain means, and test both on one run.

## What users meet

**P1. `qtest.System`: the first hour in one value.** Users wire
`GenerateRun`, `FaultScript`, MemorySink, `Progress`, `WithContext` and
`NewRecorder` by hand (connect/faults_test.go `followTraces`, 40 lines);
Explore cannot take a chooser (checkout's TestFaultOrders rebuilds it);
`qenv.WithContext(quinthegel.Chooser(tc), rec.Context(...))` repeats.
Proposal:

```go
sys := qtest.System{Binding: "checkout.binding.yaml",
	Run: func(c qenv.Chooser) error { _, err := checkout.Run(c, cfg); return err }}
sys.Explore(t, qtest.Seeds(100)).Expect(t, qtest.Conforms())
sys.Shrink(t).Expect(t, shipOrCancel)            // FindViolation, recorder wired
sys.FollowTraces(t, spec, faults).Expect(t, qtest.Conforms())
sys.Sweep(t, space, cfgOf).Expect(...)
```

`Run` returns an error, so seeds stop calling `t.Fatal` off the test
goroutine. Explore takes `Chooser func(seed) qenv.Chooser`. `quintgo
scaffold` prints the skeleton. Additive.

**P2. Quantified expectations, and `Expect` on every result.**
`Exploration.Expect` means "every run conforms" for Conforms, which is
recognised by its description string, and "one run meets all" for
anything else. That hid NoIdempotencyKey's second bug.
`quinthegel.Violation` has only a summary string, so tests use regexes
and walk reports. Proposal:
- `qtest.Some(e)`, `All(e)`, `None(e)`, `Together(a, b)`;
- `Expect` on `Violation` and on `qmeta.Recovery`;
- `quinthegel.Want(...)` for `FindViolation`.
Distinct types make old calls fail to compile, so each is reviewed:
deliberately breaking.

**P3. Check vs Validate.** `Check` fails and `Validate` doesn't, `Record`
doesn't and `RecordAndCheck` does, so `qtest.Check(...).Expect(t,
Violates(...))` can never pass. Every call should return a `*Run` that
fails only through `.Expect`. Deprecate `Check` and `RecordAndCheck`.

**P4. One recovery API.** `Recovers`, `RecoversMostly`, `Assess`,
`Mostly`, `Runs`, `Count`, `Of`, `OfSimulation`, `FindTrigger(qload.Config)`
and `SearchTrigger(System)` overlap. Keep `qmeta.System` (from `Of`,
`OfSimulation`) with `Runs` and `Mostly`, and one
`quinthegel.FindTrigger(t, sys, space)`, and make "find" return a result
judged by `Expect` (FindViolation fails t when nothing is found;
FindTrigger returns nil).

**P5. Run-declared constants.** The checkout sweep passes the stock twice,
to the app and to the model (`Opts: stockOf`); rows failed until they
agreed. Proposal: a run declares its instance (`qobs.WithRunAttrs(ctx,
"stock", 2)`, binding constants as templates `{{.run.stock}}`), or a
factor names its constant (`qsweep.Ints("stock", 1, 2).Constant("STOCK")`).

**P6. Messages for the traps.**

| Trap | Message |
|---|---|
| Constants versus sizes | "all N runs fail at step 1: the binding's constants (STOCK=2) may not match the scenario's" |
| A Script that never completes | Explore reports `Remaining()`: "scripted faults never happened: [rpc.api.request] (waiting since step 14)" |
| λ ≥ μ after calibration | "calibrated λ=10.3 ≥ μ=10: the data are mostly from a storm; hold λ (CalibrateTimeout)" |
| Unhealthy baseline | "goodput before the trigger was 0.4/s against 3 offered: set Experiment.Healthy" |
| Reproducing a seed | "reproduce with QUINTGO_SEED=17 go test -run ..." |
| "all N runs conform; want X" | Append the unfaulted kinds from fault coverage |

The healthy set is written twice, as a closure and as PromQL
(`alerts_test.go`): `qmeta.Healthy{MaxU: 10}` should give both.

**P7. Docs and skills.**
- A short `quint-start` skill with a question-to-tool table.
- Mechanics (weaving, gosim, fault points, covering arrays) move into
  references the skills load on demand.
- One shared reference for verdicts and expectations.
- The table goes at the top of the README.

**P8. One evidence report.** Name every run-producing call an
exploration, and give its result one report.

```
Evidence: checkout, 260 runs, 13 rows
  Verdicts  253 conform · 7 violate chargedAtMostOnce (rows rpc.provider=on)
  Faults    6 kinds reached, pairs 15/15, one-way 6
  Configs   pairwise 13/13 rows
  Code      76.8% · per action · after an unfaulted point: … · indistinct: …
  Mutants   48 woven, 45 killed, 3 survive in covered code
```

- `x.Coverage()` returns `qtest.Coverage{Faults, Pairs, Rows, Code,
  Mutants}`.
- Coverage gaps become expectations (`qtest.Reaches`, `NoSurvivors`).
- The report is also written as JSON next to `QUINTGO_KEEP` artifacts.

## Roadmap dependencies

- A1 and C3 (code coverage): S1 first, done; S4 next, so per-step coverage
  aligns with choices.
- A3 (fuzzing): one byte decoder in core, `qenv.FromBytes`. The rule lives
  twice today, in quinthegel's chooser and in the gosim fork.
- A2 (mutation): P2's quantifiers, to say what "killed" means.
- B1 (labels): S5.
- C1 (analyzers): S6's lint move and directive parser.
- C2 (synctest): try `NewNative(c, nil)` inside `synctest.Test` before
  adding a runtime interface.
- D3 (lag chooser): S3.

## After the roadmap (second review)

An architecture review and a design review with a first hour checked the
finished roadmap against this document. Their corrections:
- "qmeta independent of qenv" no longer holds: qmeta's recovery property
  and `Simulate` import qload, and so qenv (ROADMAP V10).
- S2: `Unwrap`/`As` done; the Recorder still special-cased in `EntropyOf`
  and `nemesisOf`, no `Layer`, `WithContext` not deprecated (V3).
- S4: `Choice.Step` done; one run record not (V4).
- S6: the lint move and `Explored.Faults` done; the two directive parsers
  and `qobs.Attributes` remain (V9).
- P1: `System.Sweep` takes a second run function; `qgosim.Scenario` does
  not fit `System.Run`; receivers differ (V4, V5).
- P3: not done; `Check` and `RecordAndCheck` still fail before `.Expect`,
  and the docs teach them (V5).
- P2/P4: `Expect` on `qmeta.Recovery` dropped; `Recovers` and `Assess`
  remain beside `System`; claims were not checked against the model's
  names (fixed: ROADMAP V0').
- P6: the constants message fires only when every run fails at step 1;
  `qmeta.Healthy` not done (V7, V13).
- P7: only quint-fault-injection split; skills still teach retired APIs
  (V5, V16).
- P8: Evidence done but terse; JSON and coverage claims not (V13).
- S3's single spelling not done: reports print `rpc.x.request`, rates
  need `fault.rpc.x.request` (V11, R2).
- New: the verdict cache keyed by steps only (fixed: V0); binding
  templates let `process`, `outcome`, `obs`, `step` overwrite arguments
  (V1); `Sim.Run` returns nil on a stall (V2); `qobs.Strict` and
  `qtest.Record`'s `SetGlobal` are process-wide (V3); the sub-modules
  cannot be installed (R1).

### Names

The toolkit outgrew "connect" (model-based testing only). Proposals, with
their proposers' picks:

| Name | For | Against | Picked by |
|---|---|---|---|
| quintet | five ways, one model; keeps Quint visible without claiming it | could read as part of the Quint project | design, streams |
| plumbline | a reference runs are checked against; CLI `plumb`; no brand borrowed | says nothing of Quint or Go | architect |
| quint-oracle | the model as every workflow's judge | long; hyphen vs package name | analyzers |
| quintgo | already the CLI, env vars and messages; no migration | reads as an official Quint binding | gRPC |
| conform | the one verdict | collides with Go projects (leebenson/conform, siderolabs/conform) | — |
| groundtruth | AGENTS' first convention | AWS SageMaker Ground Truth; long | — |
| quintessence, qgo, qkit, touchstone, assay, jig, verdict, faithful | — | generic or colliding | — |

The rename belongs in the breaking release (ROADMAP R2): the module path
changes anyway.


## Third review (after V1–V17)

Two reviews, architecture and design, with nothing released, so every
shim was in scope. The plan they led to is ROADMAP's W track; the full
reports were working files.

What they found, and where the W track fixed it:
- Bugs: escaped steps compared by count, prefixed Script kinds, `Rates.Of`
  matching, Shrink ignoring the System's options (W1).
- The process-wide global recorder installed by default (W2).
- Deprecated and internal-only exports (W3).
- Two ways to configure qtest, quinthegel and sweeps (W4).
- Two names for one concept across qload, qenv, qmeta, quinthegel and qtest (W5).
- The chooser's `fault.` prefix in user-facing kinds (W6).
- Crash defaults that hid post-commit crashes (W7).
- A first hour that starts nowhere in particular (W8).

## Fourth review (after W7)

Architecture and design again, and a planner reconciling them with the
roadmap. Both said the plan holds. They moved the rename and the gosim
split before W8, and release readiness before X. Their new findings, now
ROADMAP's M, W9 and R items:
- Published modules with `replace` directives, which cannot be consumed.
- The gosim fork under upstream's module path.
- Wire names to rename deliberately.
- quinthegel's leftovers.
- Claims' arity.
- Two configuration fields.
- Environment-variable prefixes.
- A stale Packages table and stale hello snippets.
- No license.
