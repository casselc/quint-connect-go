# Patterns from the example apps


- **Concurrent services** (`examples/kvstore`). Record each step inside the
  critical section that orders it. For spans, stamp a linearization index
  (`kv.op`) and make it an ordering key in the convert rules
  (`order: { op: attr:kv.op }`). Otherwise concurrent requests are
  ordered by span end time, which is not the order their effects took.
- **Restarts** (`examples/jobqueue`). Give every incarnation its own
  `process`, and let the binding's `restart:` insert the model's restart.
  Make operations that race a restart retry on the new incarnation, so
  nothing is recorded under a dead one.
- **Properties the design does not promise.** Leave them out of the
  binding's `invariants` and check them on demand:
  `quintgo validate -invariants exactlyOnce`, or
  `Checker.WithInvariants`. "Conforms, invariant FAIL" then says when
  and where a real run hit the design's limit.
- **Distributed systems** (`examples/glomers`):
  - Record each node's steps with the node as `thread` and a Lamport
    clock as `order.lamport`, carried in messages. Per-node step logs
    then merge correctly: `quintgo validate n1.jsonl n2.jsonl ...`.
  - Record a shared service's operations where they take effect, at the
    service, not at the client that hears back later.
  - Record collections (sets of values, vectors) as slices or maps.
    They travel as JSON, and `toSet`, `toList` and `toMap` turn them into
    Quint values in templates.
- **Model the environment you run on.** If the code depends on a
  service's guarantees (seq-kv's consistency), the model must not assume
  stronger ones. Check against the real environment too: examples/glomers
  shows two counter designs that passed a too-strong model and failed
  Maelstrom.
