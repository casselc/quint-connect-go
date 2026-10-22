# Notes for merging examples/streams

## README example-table row

| [examples/streams](examples/streams) | separate module: an outbox publisher and idempotent consumers on Kafka (franz-go, kfake) and Kinesis (AWS SDK, in-process fake), and a simulated partitioned log with lost produces/commits, rewinds, evictions mid-batch and crashes; one model and binding for all three | `commit-first`, `no-dedupe`, `non-idempotent-producer`, `off-by-one` |

## AGENTS.md

Layout line:

```
examples/streams      separate module: outbox publisher + consumers on Kafka (franz-go, kfake), Kinesis (AWS SDK,
                      kinesisfake) and a simulated log (simlog: partitions, idempotent producer, group, fencing); one binding
```

Gotchas learned:
- A step argument named `seq` (or any other step field: `actor`,
  `process`, `thread`, `outcome`, ...) is shadowed in binding templates by
  the step's own field: `{{.seq}}` is the recorder's sequence number.
  `apply(key=k2, seq=1)` replayed as `apply("k2", 4)`, and the error only
  said the step was not a transition. Name such arguments otherwise
  (`n`), or see the core fix proposed below.
- A crashable process whose restart redoes its whole job (a publisher
  that republishes everything) livelocks a run under per-step crash rates:
  every restart adds work at the same virtual instant. Give it durable
  progress (an outbox it clears on acknowledgement), as a real one has.
- `qenv.Wait` and `Signal.Wait` panic outside a simulation. A seam whose
  completion callbacks run on a real client's goroutines (kgo's promises)
  needs its own wait in the seam (`Writer.Flush`: qenv.Wait in simlog,
  `Client.Flush` in kgo), not one in the application.
- A model that learns the log from reads (instead of from appends
  recorded at the broker) checks Kafka, Kinesis and a simulated log with
  one binding: the consumer is the one place every tier can record, and
  "a partition is read without holes" makes a skipped offset a rejected
  `read`, or a commit past unapplied records.

## DESIGN section draft

### Streams on Kafka and Kinesis (examples/streams)

A publisher relays a transactional outbox to a partitioned log;
consumers apply records to a projection, deduplicated by a per-key
version, and commit offsets after a batch. One model, one binding and
three logs: qenv's simulated log, Kafka through franz-go, and Kinesis
through the AWS SDK.

**The model learns the log from reads.** A broker's appends are not
observable in every tier. A real cluster has no hook, and recording at
the producer's acknowledgement misses records whose acknowledgement was
lost. So the model takes the log from the consumers' `read(partition,
offset, key, n)`: the first read of an offset learns its record, later
reads must agree, and an offset past what was read is not a step. The
properties are over the projection (`apply`), the commits (`commit`) and
what the publisher was told (`publish`). The binding has no templates.

**A simulated partitioned log, not qenv.Queue.** qenv's Queue is
per-message acknowledgement with redelivery, and has no offsets,
partitions or groups. simlog (in the example module) is Kafka's contract:
- an idempotent producer client: sequence numbers per partition, the log
  rejecting out-of-order and dropping duplicates, 5 requests in flight;
- commits with lost requests and acknowledgements;
- rewinds;
- a consumer group with generations, eager rebalances and fencing;
- evictions as nemeses at any step (mid-batch: the zombie keeps
  applying while the new owner re-reads; the serializable projection and
  the version keep it idempotent).

It belongs in core only if a second example needs it. Kinesis's contract
is a subset (no idempotence, no groups), so it could serve that too.

**Findings while building it.**
- An argument named `seq` collides with the step's own field in binding
  templates (proposed core fix below).
- A publisher that republished everything on restart livelocked runs at
  per-step crash rates; the outbox fixed it, as it would in production.
- On a nominal run a plain test of the projection sees only `off-by-one`.
  The other three bugs need a fault, and the model names the property
  each breaks. `commit-first` needs no crash for the model:
  `committedApplied` fails at the commit itself.

**Deviations.**
- The real tiers inject no faults: kfake and the fake Kinesis run
  nominally. Their value is that the real clients, the adapters and the
  model agree.
- Kinesis runs one consumer owning every shard. Leases are the KCL's,
  with DynamoDB, and out of scope.
- Kinesis needed a fake endpoint (four calls of the JSON 1.1 protocol),
  not a Docker image (none needed: in process, like kfake).

## Timings (this machine)

- `go test -short ./...`: 93 s (streams), 42 s (kafkalog), 38 s
  (kinesislog), in parallel.
- Full: TestFaithfulConforms 60 s (80 seeds), TestMutants 183 s,
  TestHegelShrinksReorder 40 s, TestFaithfulSweep 72 s (9 rows, 90 runs).
- `QENV_CHECK=1` passes.

## Validation cost of failing runs (core, observed)

A conforming run is cheap in a batch. A failing run of 160 to 500 steps
takes 17 to 30 s to validate alone (Rust and TypeScript backends alike),
and the model's shape did not change it:
- predicates rescanning the log at every state: about 30 s;
- ghosts set at the step: about 30 s.

The time grows with the run's length, presumably locating the failing
state segment by segment. Two consequences: an exploration in which many
long runs fail (a mutant at high fault rates, 60 seeds) ran past go test's
10-minute default; and mutants are best explored at rates just high
enough to show the bug. Worth profiling in validate.

## Proposed core changes (not made)

1. binding: a step argument whose name is also a StepData field
   (`seq`, `actor`, `process`, `thread`, `outcome`, ...) should win over
   the field for a same-named parameter's default template, or Lint should
   say "argument seq of step apply is shadowed by the step's seq: name it
   otherwise or write args: {seq: '{{.args.seq}}'}". Today it is silent.
2. Optional: `simlog`'s partitioned log as a qenv double (`qenv.Log`)
   once a second example needs one.

## Skill text (quint-fault-injection, doubles section)

- **Streams (Kafka, Kinesis).** Seams: a `Writer` (Produce with a done
  callback, Flush) and a `Reader` (Poll, Commit). `examples/streams/simlog`
  is the simulated log:
  - named faults `log.produce` (lost, lostAck), `log.commit` (lost,
    lostAck) and `log.fetch` (rewind);
  - evictions as `rebalance.<consumer>` nemeses, with fencing;
  - an idempotent producer client.

  Record `read` at the consumer and let the model learn the log from it:
  the same binding then checks kfake (franz-go's in-process Kafka) and a
  fake Kinesis endpoint. A publisher should drain an outbox, so a restart
  resumes instead of redoing everything.

## CI steps

```yaml
- name: streams (Kafka, Kinesis, simulated)
  working-directory: examples/streams
  run: |
    go vet ./...
    test -z "$(gofmt -l .)"
    QENV_CHECK=1 go test -short ./...
    go test -run 'TestHegelShrinksReorder|TestFaithfulSweep' .
    go test -run '^$' -fuzz FuzzFaithful -fuzztime 30s .
```
