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
    (`time.Now`, `time.Sleep`) in files that import qenv. Mark a
    deliberate one `//qenv:ok`.
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

## 4. bis Fuzzing: Go's fuzzer drives the simulation

```go
func FuzzShop(f *testing.F) { sys.Fuzz(f, rates) }   // sys is a qtest.System
```

- Each fuzz input is one run: its bytes are the run's choices
  (`qenv.FromBytes`, the same byte rule as Hegel's chooser), and the model
  judges it. `go test` runs the corpus as regression tests; `go test
  -fuzz FuzzShop -fuzztime 60s` explores, guided by coverage of the code
  under test.
- Verdicts are cached by the run's steps (`qtest.VerdictCache`): most
  inputs repeat a run's model-level steps. On checkout, that took the
  fuzzer from 16 runs in 90 s to 14,633.
- Keep a shrunk Hegel failure, once fixed, as a seed:
  `sys.Fuzz(f, rates, must(qenv.EncodeChoices(v.Choices, qenv.DefaultRates)))`.

## 4a. Fault points in your own code, and fault coverage

A double fails the way its kind of component fails. A fault specific to
your code (a client's own error, a crash between two writes) is a fault
point:

```go
//quint:fault kind:store.write error:ErrUnavailable error:ErrConflict crash:true
func (s *Store) Write(ctx context.Context, k, v string) error
```

- Weave with qweave, with `_ "github.com/casselc/quint-connect-go/qenv/fault"`
  in the tool file. Or call `qenv.FaultPoint(ctx, "store.write", true,
  ErrUnavailable)` at the top yourself.
- In a simulation the entry becomes the choice `fault.store.write`.
  Option 0 goes on. Each other option returns one of the named errors
  (the package's own sentinels), with zero values for the other results.
  `crash:true` adds a crash of the process there, restarted as the
  nemesis restarts it. With no error named, the fault is a
  `*qenv.InjectedError`. Outside a simulation the call returns nil.
- Rates apply as to any fault kind (`Kinds: {"fault.store": 0.2}`), and
  shrinking removes the faults the failure does not need.
- **Coverage.** `x := qtest.Explore(...); t.Log(x.Coverage())`, or
  `qenv.CoverageOf(choices)` for recorded choices. It lists every fault
  kind, declared or a double's, with the runs that reached it and the
  options taken. A declared point no run reached (`Unreached`) is a
  failure path the exploration never tried, whatever the line coverage
  says. Raise its rate, or make a scenario reach it. `Pairs`, `Untried`
  and `OneWay` say which pairs of fault kinds ran together, and in which
  orders: fault-handling bugs are often two faults together.

## 4a''. Which code each step ran: `qweave -cover`

```sh
go run -C path/to/qweave ./cmd/qweave -C . -cover [-coverpkg your/module/pkg] go test -run TestX ./...
```

```go
x := qtest.Explore(t, binding, qtest.ExploreConfig{Seeds: 30, Coverage: true}, scenario)
t.Log(x.CodeCoverage()) // never run, run by no step, outcomes that ran the same code
```

- Skip the test when `len(qcover.Blocks()) == 0` (not woven).
- "Never run" after a conforming exploration: an error path whose fault
  was never taken (check `x.Coverage()`), code the model's environment
  rules out, dead code, or a scenario the model does not describe.
- "Run by no step" is code no step of its own task explains: clients
  whose steps the services record, relays, setup, or work after a task's
  last step. A worker's code there means a missing step.
- Two outcomes of an action that ran the same code: the model tells them
  apart and the code does not. That is fine when the outcome is the
  environment's (a lost response), and a question when it is the code's.

## 4a'''. What the model constrains: mutants (`qweave -mutate`, `System.Mutate`)

```sh
go run -C path/to/qweave ./cmd/qweave -C . -mutate -coverpkg your/module/pkg go test -run TestMutation ./...
```

```go
rep := sys.Mutate(t, qtest.ExploreConfig{Seeds: 20, Rates: crashy}, nil) // skips unwoven
t.Log(rep) // reached, killed, survived, unreached
```

- Every mutant (a negated `if`, a moved comparison boundary, a dropped
  call) is in one build; each is explored with the seeds, verdicts shared.
- A survivor is code the model does not constrain. Ask, for each: is it
  equivalent (a dropped ack the environment redelivers anyway), is the
  effect unobserved (a step records nothing the model compares: add the
  field and an `expect` to the binding), or is the code not needed?
- Checkout's first run had a cancel that forgot to restock survive: no
  step carried the stock. Recording it and an `expect: 'stock ==
  {{.obs.stock}}'` killed it.
- Unreached mutants are coverage's: no run reached their code.

## 4a'. Sweep the configuration space: covering arrays (`qsweep`, `qtest.SweepExplore`)

When the product of configurations grows (isolation level × sizes ×
which fault kinds happen), sweep pairwise rows instead of the product:

```go
space := []qsweep.Factor{{Name: "iso", Values: []string{"ser", "si", "rc"}}, qsweep.Ints("stock", 1, 2, 3).Constant("STOCK"),
	qsweep.Bool("crash"), qsweep.Bool("rpc.provider")}
qtest.SweepExplore(t, binding, qtest.Sweep{Factors: space, Explore: qtest.ExploreConfig{Seeds: 20},
	Expect: func(r qsweep.Row) []qtest.Expectation { ... }}, // nil: Conforms
	func(r qsweep.Row, c qenv.Chooser) { app.Run(qenv.Without(c, offKinds(r)...), cfgOf(r)) })
```

- Every pair of factor values is in some row (`Strength: 3` for
  triples), in about as many rows as the two largest factors' product.
  `Allow` rules rows out, and pairs only those rows have are not
  required.
- Fault kinds as `Bool` factors, turned off with `qenv.Without`, give
  swarm testing with a guarantee: every pair of kinds runs together, and
  each without the others masking it.
- A row's sizes need the model's matching constants: name the constant
  on the factor (`.Constant("STOCK")`) and each row is checked with its
  value. When every run fails at step 1, the report names the binding's
  constants: the usual cause.
- Expect per row: say where a mutant's bug is real. Checkout's
  `NoIdempotencyKey` sweep splits it into its two bugs: one in every row,
  and one only where the provider's responses are lost: rows without
  provider faults claim `qtest.Violates("settledCorrectly")` and
  `qtest.None(qtest.Violates("chargedAtMostOnce"))`.
- Orders of faults: a `qenv.Script` per ordered pair (`{a, b}`) imposes
  both orders of every pair, and `Remaining` names an order the system
  cannot produce. `qsweep.Sequences` (every order of t events in few
  sequences) fits only events that recur through a run, such as crashes
  and partitions: a Script waits for each kind in turn, and a phase that
  has passed does not come back.

## 4b. Code you cannot rewrite against seams: weave it

If the code uses `sync.Mutex`, `sync.RWMutex`, `sync.WaitGroup` or `go`
statements and you want it unchanged (a stand-in that must track the
real code, or the real code), weave it:
- add `_ "github.com/casselc/quint-connect-go/qenv/weave"` to the
  project's `orchestrion.tool.go`. Then test with qweave (`qweave go test
  ./...`, from the qweave module in this repository), or with
  Orchestrion (`go run github.com/DataDog/orchestrion go test ./...`);
- mark each go statement `//qenv:go` (its call must be a function
  literal). Under qweave that is all: it weaves lock method values
  (`return mu.RUnlock`) and calls promoted through an embedded lock too.
  Under Orchestrion, also turn method values into calls
  (`func() { mu.RUnlock() }`) and name embedded locks
  (`mu sync.Mutex`). `quintgo lint -weave DIR` lists what Orchestrion
  misses;
- code started in a woven goroutine uses `context.Background()` (or the
  goroutine's own context), not one captured from the task that started
  it: qenv resolves a context-less call to the goroutine's own task, and
  another task's context makes it sleep and wait as that task;
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
- **code annotated with `//quint:action`**: add a `gosim.tool.go`
  (`//go:build tools`) to the module that imports
  `github.com/casselc/quint-connect-go/qobs`. The translator then weaves
  qobs's aspects, as `orchestrion go test` would. Leave qenv/weave out:
  gosim simulates locks and goroutines itself, so code that needed weaving
  on qenv's scheduler runs as it is (examples/gosim/edgepublish).

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

## 4d. A production workload against the simulation

Replay a captured cluster's activity while the system under test runs:
`kubesim.Fleet.Replay(s, trace, speed)` (examples/fleetrollout) applies a
SimKube trace's Deployment scalings to the simulated members, as another
writer (`kubesim.AsWriter`), through the same faults. The first replay
usually shows the model has no step for the environment's other writers.
Add one, deliberately (fleet.qnt `scale`), and explore with the workload
on.

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
