# streams: publishers and consumers on Kafka and Kinesis

A stream pipeline checked end to end against one Quint model, on three
logs: a simulated one with faults (qenv), Kafka through franz-go, and
Kinesis through the AWS SDK.

- **The application** (`pipeline/`): a publisher relays keyed events from
  a transactional outbox to the log; consumers read the log, apply each
  event to a projection (idempotent by a per-key version, in one
  transaction), and commit the offset after a batch. It sees the log only
  through two seams, `Writer` and `Reader`.
- **The model** (`model/streams.qnt`) is the requirement, not a broker.
  It learns the log from the consumers' reads (a record at an offset
  never changes; partitions are read without holes) and states:
  - `settledCorrectly`: once quiet, every event the publisher was told
    is written was applied exactly once;
  - `appliedAtMostOnce`: never twice;
  - `perKeyOrder`: a key's events are applied in order;
  - `committedApplied`: no committed offset is past a record not yet
    applied.
- **The binding** (`streams.binding.yaml`) has no templates: every
  parameter is the step field of its name. The same binding checks all
  three logs.
- **The simulated log** (`simlog/`) is Kafka's contract on qenv, each
  fault a named kind with option 0 nominal:
  - a partitioned log, an idempotent producer client (5 requests in
    flight, retries kept in order by the log's sequence check);
  - `fault.log.produce` (`lost`, `lostAck`), `fault.log.commit` (`lost`,
    `lostAck`), `fault.log.fetch` (`rewind`, a duplicate delivery);
  - a consumer group with generations and fencing: `rebalance.<consumer>`
    nemeses evict a consumer at any step, mid-batch;
  - consumer and publisher crashes.
- **The real tiers**: `kafkalog/` runs on Kafka (kgo's consumer group,
  manual commits; the tests use kfake, an in-process cluster: no Docker).
  `kinesislog/` runs on Kinesis (PutRecord, GetShardIterator, GetRecords,
  checkpoints in a store). Its tests use `kinesisfake/`, an in-process
  endpoint for those calls in the SDK's JSON protocol. `tier/` is the
  harness both share.

Steps are recorded where they take effect:
- `apply` at the projection's commit;
- `commit` where the log takes it (simlog), or when the client is told it
  landed (Kafka, Kinesis's checkpoint store);
- `read` when a consumer receives a record;
- `publish` when the publisher learns a record landed and clears it from
  the outbox.

## Mutants

| Mutant | Bug | Caught by |
|---|---|---|
| `commit-first` | commits a batch before applying it: a crash between loses it | `committedApplied`, in every run (no crash needed): the commit is past unapplied records |
| `no-dedupe` | applies every record it reads | `appliedAtMostOnce`, after a redelivery (lost commit, rewind, eviction, crash, duplicate write) |
| `non-idempotent-producer` | producer idempotence off, retries and 5 requests in flight kept | `perKeyOrder`: a lost request retried behind the next record of its key |
| `off-by-one` | "last offset seen" starts at 0, skipping each partition's first record | `committedApplied`, and a plain test of the projection |

## Tests

| Test | What it shows |
|---|---|
| `TestFaithfulConforms` | 80 seeded runs with every fault kind: all conform; `Evidence` shows every kind and pair faulted |
| `TestMutants` | each mutant violates the property in its row |
| `TestPlainTestSeesOnlyOffByOne` | a test of the projection on the nominal run sees one bug of four |
| `TestHegelShrinksReorder` | Hegel shrinks the reorder to its faults; the shrunk choices replay it step for step |
| `TestFaithfulSweep` | pairwise rows of partitions × consumers × fault kinds on/off: all conform |
| `FuzzFaithful` | Go's fuzzer over the run's choices, the model as oracle |
| `kafkalog: TestKafkaConforms`, `TestKafkaMutants` | the same model on Kafka (kfake): faithful conforms, `commit-first` and `off-by-one` caught |
| `kinesislog: TestKinesisConforms`, `TestKinesisMutants` | the same on Kinesis (fake endpoint, AWS SDK) |

```sh
go test -short ./...                      # quick; skips shrinking and the sweep
go test ./...                             # all
go test -fuzz FuzzFaithful -fuzztime 60s .
./run-demo.sh                             # every backend, checked with quintgo validate
MUTATION=no-dedupe BACKENDS=sim ./run-demo.sh
```

## Notes

- Kinesis has no consumer groups or idempotent producer. Leases are the
  KCL's (DynamoDB); here one consumer owns every shard. `PutRecord` one
  record at a time keeps a key's order; a retried put after a lost
  response writes twice, which the consumers deduplicate.
- The real tiers inject no faults: kfake and the fake endpoint run
  nominally. Faults, rebalances mid-batch and crashes are the simulated
  log's. What the real tiers add is that the adapters, the real clients
  and the same model and binding agree.
