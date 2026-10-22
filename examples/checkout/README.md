# examples/checkout: a checkout saga in a simulated environment

A line-of-business app checked against its model under fault injection.
- **The shop** (package `shop`):
  - orders reserve stock;
  - a payment provider captures with idempotency keys;
  - a warehouse ships;
  - customers can cancel until shipping;
  - events between the services go through a transactional outbox and an
    at-least-once broker.
- **The environment** (package `checkout`, on [`qenv`](../../qenv)):
  - each service is a process that can crash and restart;
  - the database runs at a chosen isolation level;
  - the broker redelivers and reorders;
  - the provider is reached over a network that loses requests and
    responses, and duplicates requests;
  - clients retry.

  One `qenv.Chooser` makes every choice: a seed, Hegel, or a replay.
- **The model** (`model/checkout.qnt`) is the requirement, not the
  implementation:
  - it has no database, broker or network;
  - the provider may charge any placed order at any time;
  - its properties are `chargedAtMostOnce` (with a ghost flag) and
    `settledCorrectly`: once quiet, every order is shipped or cancelled,
    and the provider kept exactly the money of the shipped ones.

  Overselling, and shipping or cancelling an order twice, are steps the
  model cannot take.
- **Steps** are recorded with `Txn.OnCommit`, at the commit where each one
  takes effect. The provider records its own charges and refunds the same
  way.

```sh
go test ./examples/checkout/...                 # explorations of every case below, Hegel shrinking (~2 min)
examples/checkout/run-demo.sh                   # simulate, write step logs, quintgo validate them
MUTATION=lock-without-write ISOLATIONS=snapshot examples/checkout/run-demo.sh
```

## What each bug looks like, at each isolation level

`qtest.Explore` runs 60–100 seeds per case and checks them in one quint
batch. Each test states the verdict it expects with `qtest.Expect`.

| Code | Serializable | Snapshot isolation | Read committed |
|---|---|---|---|
| faithful | conforms | conforms | conforms |
| `NoLocks` (plain SELECTs) | conforms | conforms (write conflicts abort) | **rejects** `place` (oversold) or `ship`/`cancel` (lost update) |
| `LockWithoutWrite` (FOR UPDATE, results in their own tables) | conforms | **rejects** `ship` or `cancel` (write skew) | conforms (reads after the lock see the latest commit) |
| `NoIdempotencyKey` | **violates** `chargedAtMostOnce` (a retry after a lost response) or `settledCorrectly` (the void can't find the charge) | | |
| `DualWrite` (publish in the background after commit) | **violates** `settledCorrectly`: a crash before the publish loses the order (a one-step window, found at a higher crash rate) | | |

A table like this answers "harness quirk or real bug?" for each finding.
`LockWithoutWrite` is a real bug if production runs PostgreSQL at
REPEATABLE READ, or Oracle at "serializable" (which is snapshot isolation).
It is not a bug under PostgreSQL's default, READ COMMITTED.

## Findings along the way

- **The first "faithful" shop was wrong under snapshot isolation.** It
  locked the order row (`SELECT ... FOR UPDATE`), then recorded shipments
  and cancellations in their own tables. The exploration found a
  cancellation committing after a shipment. Under snapshot isolation, the
  lock makes the second transaction wait, but its other reads still come
  from its snapshot, so it doesn't see the other table's new row. The fix
  is to make the conflict a write: both transactions update the order row,
  and first-committer-wins aborts one. The old design is now the
  `LockWithoutWrite` mutant.
- **A client's retry masked the dual-write bug.** Publishing right after
  the commit lost nothing: a crash in between meant no response, so the
  client retried, and the retry published. The real bug is "commit,
  respond, publish in the background". That form is the mutant now.
- **The first `NoIdempotencyKey` failure was not a double charge.** It
  violated `settledCorrectly` instead: the void used the canonical key and
  missed the per-attempt one. `Exploration.Expect` asks whether *some*
  failing run shows the expected verdict, because a bug can show up in
  more than one way.
- **Hegel cost.** A run makes about 850 choices. Drawing each one from
  Hegel put most of a shrinking test's 300 s into Hegel calls.
  `quinthegel.Chooser` now draws entropy in 512-byte chunks, where a zero byte is
  the nominal option. Hegel shrinks the bytes toward zeros, which means
  fewer faults and the canonical schedule. The same test takes 16 s.

## Under load (`load.go`, `load_test.go`)

The same shop, with the same outbox, consumers, retries and model, runs
under steady customer traffic: 3 orders/s against a payment provider that
captures 5/s (one worker, exponential service times, `qenv.Capacity`).
The payment call times out after 1 s. Goodput is paid orders per second,
counted from the shop's own model steps. The question is whether a
transient provider slowdown leaves the shop stuck.

| Payment design | Outcome |
|---|---|
| 4 consumers | recovers from a 3x slowdown for a minute (3/3); its backlog stays small with no trigger |
| 6 or more consumers | **collapses with no trigger at all**. A few timeouts send redeliveries back, every consumer is busy, and 6 queued captures are 1.2 s of work against a 1 s timeout. Everything times out while the provider works on calls nobody waits for. The backlog grows past 1300 events in 8 minutes |
| 8 consumers + a retry budget (examples/retrystorm's `retry.Policy`) | **no help** (0/3). A capture the budget refuses goes back to the broker, which redelivers it at once: the broker is a second retry loop the budget does not see |
| 8 consumers + redelivery after 5 s | slows the storm, does not end it (0/3) |
| 8 consumers + the provider dropping calls whose caller gave up (`Capacity.Shed`, deadline propagation) | recovers (3/3) |

The threshold is the provider's capacity times the timeout: 5 capture
calls in flight. With more consumers than that, the healthy state and the
collapsed state both exist. Noise is enough to move the shop from one to
the other, so the smallest trigger Hegel could find is no trigger.

Correctness through the storm:
- **The faithful shop conforms to the model.** The test adds crashes,
  lost responses and redeliveries to the storm (`TestStormIsCorrect`, a
  1300-step run). Every capture is retried many times, and the
  idempotency key keeps each order charged once.
- **`NoIdempotencyKey` charges orders twice with no injected fault**
  (`TestStormChargesTwiceWithoutKeys`). A timeout is as ambiguous as a
  lost response, and a storm is thousands of timeouts: load is a fault
  injector.

```sh
go test -run 'Consumers|Mitigations|Storm' -v .   # ~2 min; under -short only the consumer threshold runs
```

## Layout

| | |
|---|---|
| `shop/` | the application, against `qenv.DB`, `Broker`, `Inbox`, `Payments`; mutants; the payment provider |
| `checkout.go` | the simulated deployment: processes, store, queues, provider service, clients, `Run` |
| `model/checkout.qnt`, `checkout.binding.yaml` | the model and its binding (every parameter a same-named step field) |
| `cmd/checkout-sim` | simulate seeds and write step logs |
| `checkout_test.go` | the table above as explorations; Hegel shrinking a write skew; Hegel in batches |
| `load.go`, `load_test.go` | the shop as a `qload.Experiment`: provider capacity, consumer count, retry and redelivery designs; recovery (`qmeta.Assess`) and correctness under a storm (checked with `qtest.Constants` for the load run's stock) |
