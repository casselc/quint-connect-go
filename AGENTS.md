# AGENTS.md: working on quint-connect-go

Guidance for coding agents (and humans) changing this repository. If you
are *using* the toolkit in another project, the skills in `skills/` are
for you (`quintgo skills install`).

## What this is

A Go toolkit for checking Go code against Quint models in four ways, and
its load behaviour in a fifth:
- **MBT** (`connect`): Quint traces drive the code.
- **Hegel integration** (`quinthegel`): Hegel drives and the model
  judges, or the model drives and Hegel varies.
- **Trace validation** (`qobs`, `qtrace`, `otelio`, `convert`, `binding`,
  `validate`, `qtest`): recorded runs are checked by the model.
- **Simulated environments** (`qenv`, `qtest.Explore`): the system runs
  deterministically against fault-injecting doubles, every choice made by
  Hegel or a seed, every run checked by the model.
- **Metastability** (`qload`, `qmeta`): whether a system recovers after a
  load spike or capacity dip. A CTMC predicts it, a load simulation on qenv
  confirms it, and Hegel shrinks the smallest trigger.

The design record is `docs/DESIGN.md`. Read §9 before changing `connect`
or `quinthegel`, §10 before changing `qenv`, and §11 before changing
`qload` or `qmeta`.

## Layout

```
connect/      MBT runtime (quint-connect parity); testdata/bank.qnt
quinthegel/   Hegel integration; testdata/bank.yaml
itf/          ITF decode/encode, Go <-> Quint values (Decode/Encode), Quint literal parser
qobs/ qtrace/ otelio/ convert/ load/   recording, schema/reconstruction, OTLP, span conversion, input sniffing
binding/ validate/ qtest/              binding YAML, quint-test-based validator, test helpers (Expect, Explore)
qenv/         deterministic simulation: scheduler, virtual time, processes, choosers; Store, Queue, Service doubles
qenv/weave/   Orchestrion aspects: sync locks, wait groups and //qenv:go goroutines simulated inside qenv, real outside
qenv/fault/   the //quint:fault aspect: fault points (qenv.FaultPoint) with the function's own errors, declared for coverage
qgosim/       separate module: qenv and whole programs in gosim (the fork: branch gosim-fork, ./fetch-gosim.sh)
qweave/       separate module: weaves Orchestrion aspects with types into a go -overlay (cmd/qweave); also gosim's
qload/ qmeta/ load simulation (open/closed-loop clients, servers, triggers); CTMC, recovery property, alert rules
qsweep/       covering arrays (pairwise and t-way rows with constraints, sequence covering); qtest.SweepExplore runs them
qrole/        load roles (//quint:serve, //quint:operation, //quint:call; aspects in orchestrion.yml): u and v as gauges
cmd/quintgo/  CLI
skills/       agent skills, the source of truth (embedded by package skills;
              symlinked into .agents/skills and .claude/skills, checked by skills_test.go)
examples/glomers      Gossip Glomers on a Maelstrom runtime + simulator (maelstrom/); per-challenge model, impl, binding, mutants;
                      cmd/* run under real Maelstrom (maelstrom.sh)
examples/kvstore      app: HTTP KV service with CAS (kvserver, kvload); concurrency, span conversion
examples/jobqueue     app: at-least-once queue with leases and restarts; design-level verdicts
examples/checkout     app: checkout saga on qenv (isolation levels, outbox, idempotency, crashes); cmd/checkout-sim; under load (load.go)
examples/fleetrollout separate module: multicluster-runtime controller, simulated (kubesim) and on kwok (kwok-demo.sh)
examples/crossdb      separate module: Crossplane provider (crossplane-runtime) for a dbaas, simulated and on kind with Crossplane (kind/)
examples/retrystorm   library: retry policies (backoff, budget); budget checked by a model, policies by qmeta/qload/FindTrigger
examples/bank         library example; every workflow on one small system
examples/edgepublish  separate module (Orchestrion); the spike's real-world case, model vendored in model/;
                      sim/: the publisher, unchanged, in qenv by weaving (orchestrion go test ./sim/)
examples/gosim        separate module; on gosim via qgosim: broadcast/ (Glomers node over TCP), checkout/ (the saga on
                      qenv's native runtime); simulated tests //go:build sim, host tests make the choices (DESIGN §14, §15.1)
docs/         DESIGN.md, instrumentation.md
ci/           install-quint.sh (quint + its Rust evaluator, avoiding GitHub API rate limits)
```

## Requirements and commands

- Go ≥ 1.26.1 (`go.mod`; hegel-go needs it). quint ≥ 0.32 on PATH
  (`npm i -g @informalsystems/quint`, then `QUINT_VERSION=0.33.0
  ci/install-quint.sh` for the Rust evaluator). Tests that need quint
  skip without it.
- `go vet ./... && gofmt -l .`: both must be clean.
- `go test -short ./...` takes ~1 min. `go test ./...` adds the
  shrinking-against-the-model tests, ~3–4 min.
- edgepublish (its own module):
  ```sh
  cd examples/edgepublish
  go test ./...
  go run github.com/DataDog/orchestrion go test ./mbt/ ./scenarios/   # woven
  ```
- gosim (qgosim and examples/gosim, their own modules): `qgosim/fetch-gosim.sh
  && cd examples/gosim && go test -short ./...` (the first run translates
  the standard library, about a minute). Check the simulated side with
  `go vet -tags sim`.
- fleetrollout (its own module): `cd examples/fleetrollout && go test ./...`;
  `./kwok-demo.sh` needs kwokctl and kwok on PATH (`go install
  sigs.k8s.io/kwok/cmd/{kwokctl,kwok}@v0.7.0`), no Docker (binary runtime).
- crossdb (its own module): `cd examples/crossdb && go test ./...`; `kind/demo.sh`
  needs Docker, kind, kubectl and helm (`kind/setup.sh` installs Crossplane;
  `CA_BUNDLE=` for a TLS-intercepting proxy). CRD and deepcopy are generated:
  `go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.19.0 object crd paths=./apis/... output:crd:dir=./package/crds`.
- Backend: `QUINT_BACKEND=rust|typescript` (`QUINTGO_BACKEND` also works).
  `QUINTGO_KEEP=/dir` keeps generated modules and traces from qtest and
  quinthegel.

## Conventions

- The spec is ground truth. A failing model test means fixing the code
  or, deliberately and visibly, the model. Never loosen a check to get
  green.
- Error messages say what to do next: name the step, the action and its
  picks, the field, and the fix (see `connect`'s "anonymous action" and
  "no mbt::actionTaken" errors). Tests assert on these messages, so keep
  them stable.
- Keep the core free of heavy dependencies: OTel, yaml.v3 and hegel only.
  Orchestrion stays in examples.
- Any `//quint:action` expression may only use parameters, the receiver
  and named results. Methods called only from annotations (like
  `Publisher.actor`) look unused to linters: mark them, don't delete them.
- Comments explain why, at the density of the surrounding code. Doc
  comments on every exported identifier.
- New user-facing behaviour gets:
  - a test (against `connect/testdata/bank.qnt` or
    `examples/bank/model/bank.qnt`, or a model in `testdata/`);
  - a line in README.md if it is a new entry point;
  - an update to the relevant skill in `skills/` if agents should use it.
  `skills/skills_test.go` checks the skills' front matter.
- quint invocations go through argv (never a shell). Pass absolute spec
  paths and set `cmd.Dir` to the spec's directory.
- Timing-sensitive or slow tests (shrinking against the model) skip under
  `testing.Short()`.
- Skills live in `skills/<name>/SKILL.md`, never directly in `.agents/` or
  `.claude/`, which only hold symlinks to them. Add a symlink in both when
  adding a skill (`skills_test.go` checks it). A skill's front-matter
  `name` equals its directory.
- An example app has a model in `model/`, a binding, at least one
  mutant, a `run-demo.sh`, tests showing which check catches which
  mutant, and a README with the table of tests. Its row goes in the
  README's example table. State expected verdicts with `qtest.Expect`
  (`Rejects`, `Mismatch`, `Violates`) and expected Hegel failures with
  `quinthegel.FindViolation`, not by hand. Bindings give templates only
  where a parameter is not the same-named step field.

## Gotchas

- `quint run --mbt` records every `nondet` evaluated in a step, not only
  the action's own: picks of other actions appear as `Some(..)`.
- `quint test` ITF has no `mbt::` variables.
- Generated replay modules import the model by relative path from their
  own directory. Moving a generated `.qnt` breaks its import.
- A model with no constants must be imported as `import M.*`, not `M()`
  (`binding.RenderModule` handles this).
- Hegel's `TestCase.Errorf` aborts the test case (unlike `testing.T`).
  `hegel.Test` cannot express "expected to fail": use `hegel.Run`.
- Long replays are split into `.then` segments of 20 (`binding.segmentSteps`):
  Quint's Rust evaluator rejects chains nested much past 50. A disabled
  last step is reported by quint as QNT511 at the run's own line, and a
  segment ending in one at the `.then(segK)` line. The validator maps both,
  and `validate/segments_test.go` covers them.
- A failed quint test with no error position is a tool failure (e.g. the
  evaluator crashed). The validator returns an error, never a verdict.
- Distributed examples order steps across nodes with a Lamport clock
  (`order.lamport`). Domain-clock steps with equal values are concurrent,
  and every one of them precedes every step with a larger value
  (`qtrace` links whole groups, not neighbours). A service's operations
  are recorded at the service, where they take effect.
- qenv runs are deterministic only if all nondeterminism goes through the
  Chooser: code under test must use `qenv.Go`, `qenv.Now`, `qenv.Sleep` and
  the doubles, and iterate maps in sorted order where the order matters.
  `qenv.Replay` panics on the first divergence.
- A fault point's options are nominal, then its errors in the order
  named, then the crash (`qenv.FaultPoint`). A Script's or a trace's
  option numbers depend on that order: keep it. A scripted crash is a
  nemesis by name (`nemesis.crash.<process>`): the scheduler asks a
  chooser with `ChooseNemesis` instead of choosing among enabled
  nemeses by index, which no trace can name.
- A Script imposes its faults in strict order, each waiting for its kind:
  a fault of a phase that has passed (checkout's API faults happen only
  while orders are placed) blocks every later one. Script ordered pairs,
  and keep `qsweep.Sequences` for events that recur. `qenv.Without` and
  `Swarm` keep the run's context outermost, where `New` finds it; a
  wrapper of a `WithContext` chooser must too.
- Option 0 of every qenv choice is nominal (no fault, canonical order).
  Keep that convention in new doubles: it is what makes Hegel's shrinking
  remove faults.
- `quinthegel.Chooser` draws 512-byte chunks (`hegel.Binary(n, n)`), not
  one draw per choice: per-choice draws made a shrinking test 20x slower. A task's
  panic is re-raised by `Sim.Run` with its original value, because Hegel
  aborts a test case by panicking from whichever goroutine draws.
- A nemesis (crash) is considered at every scheduling step: its rate is
  per step (`DefaultRates` 1 in 200), not per operation. Under load that
  means far more crashes per second: set `Sim.NemesisEvery`
  (`qload.Experiment` does when it injects faults).
- Simulated runs record into their own recorder. `qtest.Explore` runs
  seeds in parallel, each with a recorder in the chooser's context
  (`qenv.WithContext`, `qobs.WithRecorder`). A step without it in its
  context finds its run through its goroutine: qenv registers each
  task's goroutine and the simulation's creator (`qobs.SetFallback`,
  `qenv/goroutine.go`). Explore and Experiment runs are strict
  (`qobs.Strict`), so a step from a goroutine started with `go` panics.
  `quintgo lint -sim .` (in CI) flags `go` statements and wall-clock
  calls in files that import qenv; `//qenv:ok` exempts a line.
- qenv finds the task of a goroutine when a context carries none: Sleep,
  Wait, Go and recording with `context.Background()` from a task stay in
  its simulation (`taskOf`, `qenv/goroutine.go`). Without that, a woven
  background goroutine's simulated latency slept in real time.
- A task killed by a crash stays frozen while the simulation runs (no
  deferred code runs), and unwinds at `Close`. Steps recorded while it
  unwinds (`//quint:action` records on return) are not in the run: they
  go to `Recorder.Interrupted`, and `qtest.Explore` reports them with a
  failing run (`qobs.SetDiscard`). A task that crashes its own process
  stops in `Crash`; restart from an `OnCrash` hook.
- A step recorded by the caller on return describes what the caller saw.
  An effect the model must see whether or not its caller survives (a
  write that landed before the crash) belongs at the effect's site: the
  double or service records it.
- Woven code (`qenv/weave`): weaving rewrites calls of `sync` methods in
  the root module and `//qenv:go` go statements. A method value
  (`return st.mu.RUnlock`) or an unmarked `go` escapes, so check with
  `quintgo lint -weave DIR`. So does a promoted call through an embedded
  `sync.Mutex` (`t.Lock()` on `struct{ sync.Mutex }`): Orchestrion
  matches the receiver's type, so name the field. The lint flags the
  embedding. Inside a simulation, every user of a woven
  object must be one of its tasks (call `Close` from a task, not from the
  test goroutine). Woven calls look up the goroutine (about 1 µs), so
  weave test builds only.
- Run qenv's tests, and tests of new doubles, with `QENV_CHECK=1`: the
  scheduler checks its invariants at every step. CI does.
- A recovery verdict needs a healthy baseline (`Experiment.Healthy`). A
  system already collapsed before the trigger "recovers" to its collapsed
  goodput otherwise.
- Libraries that read the real clock (crossplane-runtime's create
  annotations and grace period) see no virtual time passing. Translate
  their timestamps at the seam (`Sim.ToVirtual` on write, `Sim.ToReal` on
  read: crossdb's kube interceptor), or their time-based logic is not
  exercised (a grace period that never expires blocked every deletion).
- One thread per goroutine when recording with a domain clock: a thread
  asserts program order, and goroutines sharing one contradict it
  (`issue: error cycle`).
- `FileSink` buffers: a killed process loses its last steps, undetectably.
  Use `DurableFileSink` where crashes are part of the test.
- A `qenv.Signal` waiter is tagged with the task's wait generation; a
  timed-out wait must not be woken by a later `Notify` (it once shortened
  the next sleep). Keep the tag when touching the scheduler.
- qload metrics are time series of rates; sample counters reset every
  tick. `Sample.Orbit` is known only in hindsight (an attempt is in the
  orbit from its start if it times out) and is filled when the run ends.
- An alert region is defined by the probability of recovering within the
  bound (`RecoveryWithin`), not the mean hitting time, which near a storm
  is dominated by rare tips into it. Calibrate a running system with
  `CalibrateTimeout` (λ held): a fit of λ to storm data drives it to μ.
- `qmeta`'s chain ranks policies; its absolute times are not the
  simulation's. Keep its orbit bound well above the storm's size, and
  assert orderings or orders of magnitude, not seconds.
- Controllers are closed loops (`ClientConfig.Population`). An open-loop
  abstraction double-counts a failing object's polls and finds storms
  that cannot happen.
- `quinthegel.CaseChecker` prefix reuse is only sound for one actor with
  no `order.*` keys, where the recording order is the reconstructed order,
  and with equal instance constants. Keep those guards.
- gosim (`qgosim`, `examples/gosim`) translates everything the simulated
  package imports: keep Hegel and other native code out of it. Hegel
  adapters live in quinthegel (`Chooser`, `FindTrigger`,
  `SearchTrigger`), and the Glomers one in `maelstrom/hegelnet`, never
  next to code a simulation runs. `internal/simdeps` fails if qenv, qobs,
  qtrace, qload, qmeta or a simulated example depends on Hegel, purego or
  cgo, and names the import chain. It writes the
  translation into the module's `.gosim/`, which `gofmt -l .` at the root
  reports: it is gitignored, ignore it.
- qenv's native runtime (`qenv.NewNative`) keeps one-task-at-a-time
  semantics with a lock the running task gives up at seams, and a
  condition variable broadcast when it does. A waiter that wakes and is
  not ready must not broadcast again: two waiters would wake each other
  forever, and gosim's virtual time would never pass. Its processes'
  tasks start on the process's machine (a spawn queue its main goroutine
  serves), and a machine crash is requested before the crash hooks run,
  which may restart it.
- The gosim fork is the `gosim-fork` branch of this repository: upstream
  history plus our commits, its `FORK.md` saying what they change.
  `qgosim/fetch-gosim.sh` pins the commit qgosim needs (`REF`): change the
  fork there, push the branch, then bump `REF`. Choosers with entropy
  (`qenv.EntropyOf`: Seeded, quinthegel.Chooser, a Recorder or Replay of
  them) hand gosim bytes in chunks for every choice but faults. A chooser
  without it (a plain wrapper) falls back to one round trip per choice,
  about 9 times slower.
- Weaving is qweave's (its own module, x/tools): `qweave go test ./...`
  weaves the package graph with the aspects of the packages the tool file
  imports and runs the go command with `-overlay`; qgosim hands gosim the
  same overlay (`GOSIM_OVERLAY`). For gosim, the module's `gosim.tool.go`
  (else `orchestrion.tool.go`) names them: list qobs for `//quint:action`,
  never qenv/weave (gosim simulates sync itself). qweave weaves method
  values and promoted calls that Orchestrion misses; `quintgo lint -weave`
  is about Orchestrion builds. A new join point or advice goes in
  `qweave/weave.go`, with a fixture test. On the native runtime with a
  Platform, qenv takes no lock across user code (gosim is cooperative):
  never put a real lock or a multi-send loop where a crash or a goroutine
  switch could strand it (DESIGN §15.8), and compare times without
  monotonic readings (`Sim.Now` already drops them).
