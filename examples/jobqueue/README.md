# examples/jobqueue: an at-least-once job queue, its restarts, and a design property

A job queue with leases:
- workers lease pending jobs, perform them (a side effect), and ack them;
- a lease that runs out returns the job to the queue, while its worker may
  still be working;
- the queue process can restart: its leases are in memory and are lost,
  while a journal keeps the jobs and which are done.

The pieces:
- `queue/`: the service and the workers;
- `cmd/jobqueue`: a demo run with concurrent workers, slow jobs and a
  restart half-way through;
- `model/jobqueue.qnt`: the Quint model.

```sh
./run-demo.sh                          # conforms; exactlyOnce, checked explicitly, fails where a job ran twice
MUTATION=ack-any-lease ./run-demo.sh   # accepts an ack whose lease was lost: rejected at that ack
MUTATION=forget-done ./run-demo.sh     # acks not journaled: a done job is leased again after the restart
```

```
$ quintgo validate -binding jobqueue.binding.yaml steps.jsonl
  conformance: PASS
  invariant doneImpliesPerformed: PASS
$ quintgo validate -binding jobqueue.binding.yaml -invariants exactlyOnce steps.jsonl
  conformance: PASS
  invariant exactlyOnce: FAIL
    violated after step [58]
      quint:    perform("w2", 4)
```

## What it shows

- **All three kinds of verdict.**
  - An implementation bug: the run is **not a behaviour of the model**.
    `ack-any-lease` and `forget-done` show this.
  - A design property: the run **is a behaviour of the model, and a model
    invariant fails on it**. The model's `exactlyOnce` is something an
    at-least-once queue does not promise, so the binding leaves it out.
    Checking it with `-invariants exactlyOnce` (`Checker.WithInvariants`)
    shows when, in a real run, a job was performed twice.
  - Everything holds.
- **Restarts.** Each queue incarnation records its steps with its own
  `quint.process`. The binding's `restart: restart` inserts the model's
  restart action where the incarnation changes, and the restart is never
  recorded explicitly. An operation that races with a restart retries on
  the new incarnation (`Service.lock`), so no step is recorded under a
  dead one. Reconstruction would report such a step as an overlap.
- **Steps from two kinds of party.** Workers' side effects are
  reported through the queue (`Service.Performed`), so they are ordered
  with the queue's own steps and carry the current incarnation.
- **When model-driven testing does not fit.** The model lets a lease take
  *any* pending job, while the queue always leases the oldest. Model
  traces would ask the queue for leases it cannot perform: the
  implementation is a refinement that the model's choices cannot
  control. So this example checks the code's own runs instead, recorded
  from scripted scenarios and from Hegel. Compare examples/bank and
  examples/kvstore, whose models and code agree on every choice.

## Tests (`jobqueue_test.go`), on a manual clock

| Test | Shows |
|---|---|
| `TestSlowWorkerConformsButRunsTwice` | a slow worker's lease expires and the job runs twice: the run conforms, and `exactlyOnce` fails at the second perform |
| `TestRestartConforms` | a restart loses a lease: the ack after it is stale, and the restart is inferred |
| `TestMutantsRejected` | both mutants, rejected at the step the bug makes |
| `TestHegelRunsConform` | Hegel interleaves enqueues, leases, work, time and restarts; every run conforms |
| `TestHegelShrinksAckAnyLease` | Hegel shrinks the mutant to: enqueue, lease, restart, perform, ack accepted |
