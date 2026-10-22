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
- **Workload choices** (cancel or not, when) are schedule choices:
  `s.Choose("sched.client.cancel", 10)`.
- **Write a `Run(chooser, Config)`** for the whole deployment, so tests,
  Hegel and a CLI that writes step logs all share it.

## 4. Explore, then shrink

```go
qtest.Explore(t, "app.binding.yaml", qtest.ExploreConfig{Seeds: 100, Rates: rates}, func(c qenv.Chooser) {
	app.Run(c, cfg)
}).Expect(t, qtest.Violates("chargedAtMostOnce"))                    // or Conforms(), Rejects(a), AnyOf(...)
```

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
    (`time.Now`, `time.Sleep`) in files that import qenv. Mark a
    deliberate one `//qenv:ok`.
- **Other instances:** `qtest.Constants(map[string]string{"STOCK":
  "1000"})` checks against the model with other constants, so one
  binding serves runs of every size.
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
  `quinthegel.FindViolation(t, cfg, func(tc, rec) { app.Run(qenv.WithContext(quinthegel.Chooser(tc), rec.Context(ctx)), cfg) })`
  shrinks a failure to the interleaving and faults it needs; faults
  appear as notes. Skip it under `testing.Short()`.
- **Extending qenv** (a new double, a scheduler change): run its tests
  with `QENV_CHECK=1` (or `s.Check = true`). The scheduler then verifies
  its own invariants at every step. `TestSchedulerSemantics` states its
  semantics as a Hegel property over random programs.
- **Profiles:** run each property under every contract you might run
  on. For each mutant, a row per isolation level (or store, or broker
  mode) says where the bug is real (see examples/checkout's table).

## 4b. Code you cannot rewrite against seams: weave it

If the code uses `sync.Mutex`, `sync.RWMutex`, `sync.WaitGroup` or `go`
statements and you want it unchanged (a stand-in that must track the
real code, or the real code), weave it:
- add `_ "github.com/casselc/quint-connect-go/qenv/weave"` to the
  project's `orchestrion.tool.go`, and test with `go run
  github.com/DataDog/orchestrion go test ./...`;
- mark each go statement `//qenv:go` (its call must be a function
  literal), turn lock method values into calls
  (`func() { mu.RUnlock() }`), and name embedded locks
  (`mu sync.Mutex`, not `sync.Mutex`): promoted calls are not woven.
  `quintgo lint -weave DIR` lists all three;
- pass the code its clock if it takes one (`Now: s.Now`);
- skip simulation tests in unwoven builds. A `//quint:action` probe that
  records only when woven tells you which build you are in
  (examples/edgepublish/sim `Woven`).

Inside a simulation the woven locks are scheduling points, so seeds and
Hegel explore their interleavings. Outside one, the real methods run.

## 4c. A whole program, network and channels included: gosim

When channels, `select`, the standard library's own goroutines, TCP or the
disk matter, run the code in gosim. Use package qgosim, its own module, on
a patched gosim fetched by `qgosim/fetch-gosim.sh`; examples/gosim shows
both kinds of code:
- **a qenv deployment, unchanged**: in a simulated test, `checkout.RunOn(qgosim.New(), cfg)`.
  `qgosim.New` is qenv's native runtime (`qenv.NewNative`):
  - tasks are goroutines;
  - processes are gosim machines, and a crash stops all of a machine's
    goroutines;
  - time is gosim's;
  - choices are the host's.

  Give a `Run`-style function a `*qenv.Sim` parameter so that both
  runtimes can call it.
- **any program**: start machines with `gosim.NewMachine`, and ask for
  faults with `gosimruntime.Choose("fault.<name>", n)`, where option 0
  is no fault. Record with qobs into `qgosim.Sink()`.

On either kind:
- put the simulated test under `//go:build sim`, and its host test under
  `//go:build !sim` when they share a package;
- keep Hegel and anything native out of the simulated package's imports
  (gosim translates them all). qenv, qobs, qload and qmeta have none:
  Hegel adapters live in quinthegel (`quinthegel.Chooser`,
  `quinthegel.FindTrigger`). Do the same with your own: Hegel code in a
  package the simulation does not import;
- drive it from the host with `qgosim.Run(t, pkg, test, chooser,
  env...)`, which returns the steps. The chooser can be
  `qenv.NewSeeded`, `quinthegel.Chooser(tc)`, a `qenv.Recorder` or `qenv.Replay`.
  Or use `qtest.Explore(t, binding, x, qgosim.Scenario(t, pkg, test))`,
  which runs seeds in parallel, one test binary each;
- check the steps with `qtest.Check` or `qtest.Validate`, or add them
  with `rec.Add(steps...)` inside `quinthegel.FindViolation`.

Each choice is a round trip to the test process, about 50 µs, so a gosim
run is far slower than one on qenv's scheduler. Explore and shrink there,
and use gosim to confirm with the real standard library and network.

## 5. Take the same model to the real environment

Run the real binary against real dependencies, and inject the faults you
can. For Kubernetes: kwok clusters with the binary runtime (no Docker),
`kill -9` the controller, `kubectl` to reset. Write step logs and check
them:
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
