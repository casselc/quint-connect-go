---
name: quint-fault-injection
description: Test Go services against a Quint model under deterministic fault injection with quint-connect-go's qenv package - write the app against seams (qenv.DB/Txn, broker and service interfaces, qenv.Go/Now/Sleep), deploy it into a simulated environment (crashable processes, a store at Serializable/SnapshotIsolation/ReadCommitted, an at-least-once queue, lossy RPC, fake Kubernetes API servers), explore seeds with qtest.Explore or shrink with Hegel (qenv.Hegel + quinthegel.FindViolation), and carry the same model to a real environment (kwok, Maelstrom-style). Use when a system's correctness depends on retries, idempotency, outboxes, isolation levels, crashes, status lag or lost responses, or when a bug only shows up in a real environment.
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
qobs.Record(context.Background(), "settle", nil)
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
- **Rates:**
  - A nemesis (crash) is considered at every scheduling step, so its rate
    is per step (default 1 in 200).
  - A bug with a one-step window needs a higher rate:
    `Kinds: {"fault.nemesis": 0.05}`.
  - Turn whole kinds off with `{"fault.kube": 0}`, or per run with
    `qenv.Swarm`.
- **Shrinking:**
  `quinthegel.FindViolation(t, cfg, func(tc, _) { app.Run(qenv.Hegel(tc), cfg) })`
  shrinks a failure to the interleaving and faults it needs; faults
  appear as notes. Skip it under `testing.Short()`.
- **Profiles:** run each property under every contract you might run
  on. For each mutant, a row per isolation level (or store, or broker
  mode) says where the bug is real (see examples/checkout's table).

## 5. Take the same model to the real environment

Run the real binary against real dependencies, and inject the faults you
can. For Kubernetes: kwok clusters with the binary runtime (no Docker),
`kill -9` the controller, `kubectl` to reset. Write step logs and check
them:
`quintgo validate -noitf -binding app.binding.yaml logs/*.jsonl`.
- A new process is a restart (`restart:` in the binding).
- A final fresh read of every component, then `settle`, makes eventual
  properties meaningful there too.

## What the examples found

- **Locking.** A lock on a parent row doesn't stop write skew under
  snapshot isolation, if the conflicting writes go to other tables. Make
  the conflict a write.
- **Retries.** A client's retry can mask a dual-write bug. The realistic
  form is "commit, respond, publish in the background".
- **In-memory tracking.** It breaks under lost responses, not just
  restarts: an update applied with its response lost is never tracked.
- **Status.** Trusting a status without `observedGeneration >=
  generation` overruns a rollout budget, both simulated and on real kwok.
