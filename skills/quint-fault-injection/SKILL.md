---
name: quint-fault-injection
description: Test Go services against a Quint model under deterministic fault injection with quint-connect-go's qenv package - write the app against seams (qenv.DB/Txn, broker and service interfaces, qenv.Go/Now/Sleep), deploy it into a simulated environment (crashable processes, a store at Serializable/SnapshotIsolation/ReadCommitted, an at-least-once queue, lossy RPC, fake Kubernetes API servers), explore seeds with qtest.Explore or shrink with Hegel (quinthegel.Chooser + quinthegel.FindViolation), and carry the same model to a real environment (kwok, Maelstrom-style). Use when a system's correctness depends on retries, idempotency, outboxes, isolation levels, crashes, status lag or lost responses, or when a bug only shows up in a real environment.
---

# Fault injection with qenv

A fault is a choice the environment's contract allows:
- a response lost after the request took effect;
- a message redelivered;
- a read from a snapshot;
- a crash between two statements.

`qenv` runs the system deterministically and makes every such choice
through one `Chooser`. Option 0 of every choice is nominal, so Hegel's
shrinking removes faults.

## 1. Write the app against seams

- **Data:** `qenv.DB` and `qenv.Txn` (Get, GetForUpdate, Put, Delete,
  Scan, Commit, Rollback, OnCommit). In production, an adapter over
  `database/sql` implements them.
- **Other services:** small interfaces of your own (a broker, a payment
  client), implemented in the deployment over `qenv.Queue` and
  `qenv.Service`.
- **Concurrency and time:** `qenv.Go`, `qenv.Now` and `qenv.Sleep`, not
  `go`, `time.Now` or `time.Sleep`. Outside a simulation they fall through
  to the real thing.
- **Iteration:** iterate maps in sorted order wherever the order affects
  behaviour.
- **Kubernetes controllers:** take readers and writers per cluster
  through an interface (`Clusters`). In simulation, give them
  controller-runtime fake clients behind `interceptor.Funcs`; that's the
  seam where generation, conflicts, lag and lost responses happen
  (examples/fleetrollout `kubesim`).

## 2. Record steps where effects happen

- **A commit:** `tx.OnCommit(func() { qobs.Record(ctx, "place", nil, "order", id, "qty", n) })`.
  The hook runs when the commit is applied, even if the caller is told
  the outcome is unknown.
- **A remote service** records its own operations (a payment provider's
  charges and refunds).
- **A fake API server** records patches as it applies them. With real
  clusters the controller records them when the update succeeds.
- **Evidence:** record every read the code decides on, with its version
  (`resourceVersion`), and let the model order evidence by it.
- **On return, from the caller** (`//quint:action`): the step is what
  the caller saw, and a crash before the return means no step. A crashed
  task's deferred recording at `Close` goes to `Recorder.Interrupted`,
  reported with a failing run but not checked. If the model must see an
  effect whether or not its caller survives (a write that landed, then a
  crash), record it at the effect's site instead.
- **Crash tests in a real environment** need `qobs.DurableFileSink`.
  `FileSink` buffers, and the steps it loses at the end of a log are
  undetectable.

## 3. Deploy into a simulation

```go
s := qenv.New(chooser)
defer s.Close()
db := qenv.NewStore(s, "shop", cfg.Isolation)
q := qenv.NewQueue(s, "payments")
provider := qenv.NewService(s, "provider", nil, p.Handle)          // its own process, never crashes
api := s.Process("api", true, func(context.Context) {})              // crashable; handlers run in it
s.Process("payments", true, func(ctx context.Context) { app.RunPayments(ctx, inbox{q}) })
s.Spawn("client", func(ctx context.Context) { /* calls that retry on qenv.ErrTimeout */ })
s.Run(20 * time.Second)       // with faults
s.Settle(60 * time.Second)    // faults off, canonical schedule, until quiet
qobs.Record(s.Context(), "settle", nil)
```

- **Restarts:** a process's start function runs on boot and after every
  restart. It must rebuild from durable doubles only; in-memory state is
  lost, which is the point.
- **Stuck runs:** `Run` and `Settle` return `qenv.ErrStuck`, naming the
  tasks, when a task waits with nothing left to wake it (a lost message, a
  missed Notify). Return the error from your `Run`; a task that may wait
  forever by design (a server loop) calls `qenv.Daemon(ctx)`, and a
  double's own receive waits with `qenv.WaitIdle`.
- **Workload choices** (cancel or not, when) are schedule choices:
  `s.Choose("sched.client.cancel", 10)`.
- **Start from a scaffold:** `quintgo scaffold -system -binding
  app.binding.yaml -pkg app` prints a System test whose Run deploys
  nothing yet, a commented `qobs.Record` per action with the fields the
  binding reads, a conformance test that fails until steps are recorded,
  and a mutant test to fill in. `examples/hello` is the smallest one
  filled in.
- **Write a `Run(chooser, Config)`** for the whole deployment, so tests,
  Hegel and a CLI that writes step logs all share it.

## 4. Explore, then shrink

```go
sys := qtest.System{Binding: "app.binding.yaml", Run: func(c qenv.Chooser) error { return app.Run(c, cfg) }}
sys.Explore(t, qtest.ExploreConfig{Seeds: 100, Rates: rates}).Expect(t, qtest.Violates("chargedAtMostOnce")) // or Conforms(), Some/All/None(...)
quinthegel.Shrink(t, sys, hegel.WithTestCases(50)).Expect(t, qtest.Violates("chargedAtMostOnce"))
```

- **One System, every workflow:** `sys.Explore` (seeds), `sys.Sweep`
  (covering rows), `sys.FollowTraces` (a model's traces' faults),
  `quinthegel.Shrink` (Hegel), all from the same `Run`. `Run` returns an
  error when the system could not run, which fails the test and names
  the seed: never `t.Fatal` inside it, since a seed's goroutine is not
  the test's. `ExploreConfig.Chooser` gives each seed its own chooser (a
  Script, `qenv.Replay(v.Choices)` of a shrunk failure).

- **Batching:** every seeded run is checked in one quint batch. On a
  miss, the report names the seed, its faults and its step log.
- **Parallel seeds:** seeds run in parallel (`ExploreConfig.Parallel`,
  default GOMAXPROCS), each with its own recorder in the chooser's
  context (`qenv.WithContext`). So:
  - build the simulation from the chooser you are given;
  - keep no state across runs;
  - start goroutines with `qenv.Go`, never `go`. A step lands in its run
    from any of the simulation's goroutines (its tasks, the one that
    created it), whatever context it was recorded with. From a `go`
    goroutine it has no run, and the exploration panics with the fix.
  - Run `quintgo lint -sim .` to find `go` statements and wall-clock calls
    (`time.Now`, `time.Sleep`) in files that import qenv, tests included
    (a System's `Run` closure lives there). Mark a deliberate one
    `//qenv:ok <why>`.
- **Other instances:** `qtest.Constants(map[string]string{"STOCK":
  "1000"})` checks against the model with other constants, so one
  binding serves runs of every size.
- **Reproducing a seed:** a failing run's report ends with
  `reproduce: QUINTGO_SEED=17 go test -run '^TestX$'`; with QUINTGO_SEED
  set, an exploration runs that seed alone. A run whose Script never made
  all its faults lists them ("scripted faults that never happened").
- **Rates:**
  - A nemesis (crash) is considered at every scheduling step, so its rate
    is per step (default 1 in 200). Under load, steps per second grow a
    hundredfold, and so would crashes. Set `s.NemesisEvery = time.Second`
    to make the rate per virtual second (`qload.Experiment` does this
    when it injects faults).
  - A bug with a one-step window needs a higher rate:
    `Kinds: {"fault.nemesis": 0.05}`.
  - Turn whole kinds off with `{"fault.kube": 0}`, or per run with
    `qenv.Swarm`.
- **Shrinking:**
  `quinthegel.FindViolation(t, cfg, func(tc, rec) { app.Run(rec.Env(quinthegel.Chooser(tc)), cfg) })`
  shrinks a failure to the interleaving and faults it needs; faults
  appear as notes. Skip it under `testing.Short()`.
  `rec.Env` records the run's choices too: `qenv.Replay(v.Choices)`
  repeats the shrunk failure outside Hegel, a regression test with no
  Hegel in it. State the failure with `v.Expect(t, qtest.Rejects(...))`.
- **A new double's faults:** declare the kind's options by name,
  `var request = qenv.Faults("db.request", "unavailable", "lostResponse")`,
  and choose with `s.FaultOf(request)` (it returns "" or the option's
  name). A read that cannot lose a response takes
  `request.Only("unavailable")`, a prefix. Scripts name the option
  (`qenv.Scripted{Kind: "db.request", Name: "lostResponse"}`), which
  waits for a choice that offers it, and coverage lists options by name.
- **Extending qenv** (a new double, a scheduler change): run its tests
  with `QENV_CHECK=1` (or `s.Check = true`). The scheduler then verifies
  its own invariants at every step. `TestSchedulerSemantics` states its
  semantics as a Hegel property over random programs.
- **Profiles:** run each property under every contract you might run
  on. For each mutant, a row per isolation level (or store, or broker
  mode) says where the bug is real (see examples/checkout's table).

## More, when the task needs it

Read the reference before using what it covers:
- [`references/services.md`](references/services.md): HTTP servers, gRPC services (`simgrpc`, otelgrpc spans) and Kafka/Kinesis consumers and publishers (a simulated partitioned log, kfake, a fake Kinesis): the seams and examples.
- [`references/fuzzing.md`](references/fuzzing.md): coverage-guided exploration over the chooser's bytes (`System.Fuzz`), a shrunk failure as a corpus entry.
- [`references/fault-points.md`](references/fault-points.md): your own functions as fault points (`//quint:fault`, `qenv.FaultPoint`), and which faults runs reached.
- [`references/coverage-and-mutants.md`](references/coverage-and-mutants.md): which code each step ran (`qweave -cover`), the one-report `Evidence`, and mutants (`qweave -mutate`, `System.Mutate`).
- [`references/sweeps.md`](references/sweeps.md): isolation levels, sizes and fault kinds swept by pairwise rows (`qsweep`, `System.Sweep`), factors as model constants.
- [`references/weaving.md`](references/weaving.md): third-party or legacy code with sync locks and goroutines, simulated by weaving (qweave, Orchestrion).
- [`references/gosim.md`](references/gosim.md): a whole program, its network and channels, simulated by gosim (qgosim).
- [`references/workloads.md`](references/workloads.md): a production workload (SimKube traces, `qload.Piecewise`) replayed against the simulation.
- [`../quint-verification-workflow/references/verdicts.md`](../quint-verification-workflow/references/verdicts.md): every verdict and expectation (`Rejects`, `Mismatch`, `Violates`, `Some`/`All`/`None`) and what each means.

## 5. Take the same model to the real environment

Run the real binary against real dependencies, and inject the faults you
can. For Kubernetes: kwok clusters with the binary runtime (no Docker),
`kill -9` the controller, `kubectl` to reset. To give a kwok cluster the
timing of production, generate pod Stages from a SimKube trace (`go run
./cmd/kwok-stages -trace f.sktrace -speed 10 > stages.yaml`, then
`kwokctl create cluster --config stages.yaml`; fleetrollout's `TRACE=`),
from the same quantiles as the simulated `PodTimes`. Write step logs and
check them:
`quintgo validate -noitf -binding app.binding.yaml logs/*.jsonl`.
- A new process is a restart (`restart:` in the binding).
- A final fresh read of every component, then `settle`, makes eventual
  properties meaningful there too.

## Third-party controller logic (Crossplane, controller-runtime)

Run the library's real reconciler in the simulation. Don't copy its
logic.
- Give it a manager stub (`GetClient`, `GetScheme`) whose client is a
  fake behind `interceptor.Funcs`.
- Libraries that read the real clock need their timestamps translated at
  that seam: `Sim.ToVirtual` on writes, `Sim.ToReal` on reads and
  responses, for Get, List, Create, Update and Patch. Otherwise their
  timeouts and grace periods never expire.
- Record cluster state with an audit after quiescence (phase, Secrets).
  The same audit works against a real cluster (examples/crossdb).

## What the examples found

- **Locking.** A lock on a parent row doesn't stop write skew under
  snapshot isolation, if the conflicting writes go to other tables. Make
  the conflict a write.
- **Retries.** A client's retry can mask a dual-write bug. The realistic
  form is "commit, respond, publish in the background".
- **In-memory tracking.** It breaks under lost responses, not just
  restarts: an update applied with its response lost is never tracked.
- **External names.** A Crossplane provider whose external name is the
  cloud's generated ID leaks a database after one lost response and a
  delete, with no crash. Make the name deterministic (a client token) and
  the create idempotent; keep the creation grace period for eventually
  consistent clouds.
- **Status.** Trusting a status without `observedGeneration >=
  generation` overruns a rollout budget, both simulated and on real kwok.
