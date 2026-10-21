# examples/glomers: Gossip Glomers, with models

Worked solutions to Fly.io's [Gossip Glomers](https://fly.io/dist-sys/)
distributed-systems challenges, each with:
- a Quint model of what the challenge asks for, with its properties as
  invariants;
- an implementation that records its model steps;
- a binding, and mutants that inject real bugs;
- tests that show which check catches which bug.

The same nodes run under real [Maelstrom](https://github.com/jepsen-io/maelstrom),
and the step logs they write there are checked against the same models.

| Challenge | Model (`model/`) | Properties | Bugs (mutants) and how they show up |
|---|---|---|---|
| 1 echo | (runtime test) | | |
| 2 unique ids | `uniqueids.qnt`: any node may answer with any id | `idsUnique` | `CounterOnly`, `NoIncarnation` (a restart repeats ids): conforms, **idsUnique FAIL** at the repeat |
| 3 broadcast (3a–3e) | `broadcast.qnt`: any gossip strategy, lost messages, reads return what the node knows | `noInvention`; `convergedWhenSettled` | `ReadOwnOnly`: **state mismatch** at a read; `NoRetry` (fire and forget): conforms, **convergedWhenSettled FAIL** at settle. Hegel shrinks NoRetry to *broadcast, its one gossip lost, settle* |
| 4 grow-only counter | `gcounter.qnt`: a G-counter CRDT, vectors gossiped and merged by max | `noOvercount`; `countsEveryAdd` | `Overwrite` (merge by overwriting): a read the model **cannot take**; `GossipOnChange`: conforms, **countsEveryAdd FAIL** |
| 5 kafka-style log (5b, 5c) | `kafka.qnt`: logs in lin-kv appended by CAS; polls, commits, listings | `sentAtItsOffset`, `offsetsUnique`, `commitsInLog` | `BlindAppend`: conforms, **sentAtItsOffset FAIL** (an acknowledged message overwritten); `PollSkips`: **state mismatch** at a poll |

```sh
go test ./examples/glomers/... -short        # sim, scripted and Hegel-scheduled runs (~2 min)
go test ./examples/glomers/... -run Shrink   # + Hegel shrinking a mutant against the model
examples/glomers/maelstrom.sh                # under real Maelstrom (Java 11+, gnuplot), then quintgo on the step logs
```

## How it fits together

**`maelstrom/`** is a small Maelstrom runtime and simulator:
- **`Node`** speaks Maelstrom's protocol: handlers, `Reply`, `RPC` with
  callbacks, timers. `Run` serves stdin and stdout under Maelstrom.
  - `Node.Step` records a model step. The node is the step's thread
    (program order), and a **Lamport clock** is the ordering key across
    nodes (`order.lamport`). The clock travels in every node-to-node
    message body. Maelstrom rejects extra keys in replies to clients and
    services, so those don't carry it.
  - Under Maelstrom each node writes its own step log
    (`$QUINT_STEPS_DIR/<node>.jsonl`). `quintgo validate` merges the logs
    by those clocks, so a delivery is ordered after its send.
- **`Sim`** is a deterministic in-process network. Nothing happens unless
  the test says so:
  - deliver message *i*, or drop it;
  - partition and heal, as Maelstrom's partition nemesis does;
  - fire a node's timers;
  - restart a node;
  - `Settle`: heal, then rounds of ticks and deliveries until quiet.
    `Sim.Step("settle")` records that moment, which the models'
    convergence properties are conditioned on.
- **`Network`** is a Hegel state-machine fragment whose rules deliver,
  drop, partition, heal and tick. Embed it in a challenge's machine next
  to its client rules, and Hegel explores schedules, faults included,
  then shrinks a failure to the shortest schedule.
- **`KV`** is Maelstrom's lin-kv. It records its own operations as model
  steps at the moment they take effect (`Name` maps store keys to model
  keys and actions): an operation's linearization point is at the
  service, not at the node that only learns of it when the reply comes.
  Kafka's model uses these steps.

**Choosing what the model allows.** Each model is a specification of the
challenge, not of one solution:
- the broadcast model lets any node gossip any subset of what it knows
  to anyone, so batching, a spanning tree and retries are all among its
  behaviours (`broadcast.Tree(4)` gossips on a tree, 3e's budget);
- lost messages are simply never delivered;
- properties the challenge demands but no single step can show are
  invariants conditioned on `settled`: eventual delivery for broadcast,
  a complete count for the counter.

## Findings along the way

- **The counter needed three tries, and the model was wrong twice.**
  1. The first design kept the counter in one seq-kv key, added with
     read and CAS, and read it with a plain read. Maelstrom's checker
     caught stale final reads (`1263` when the total was `1267`). The
     model and the Sim had both assumed a linearizable store.
  2. The second design confirmed each read with a CAS of the value onto
     itself, and modelled seq-kv's stale reads. The Sim and the model
     passed, and Maelstrom still failed one run in three. Its docs say
     why: on seq-kv "update operations may interact with past states", so
     a CAS can succeed against an old state too.
  3. The G-counter CRDT doesn't depend on the store's consistency at all.
     It passes Maelstrom, and its Maelstrom step logs (1296 steps from
     three nodes, with partitions) conform to its model.

  The lesson is to model the environment you actually run on, or design
  so you don't depend on it, and to check against the real environment
  as well.
- **Hegel found a gap in the "faithful" kafka node.** It accepted a
  commit beyond the end of the log, violating `commitsInLog`. Maelstrom's
  workload only commits offsets it polled, so Maelstrom would never have
  found it. The node now ignores such commits (a log only grows, so
  checking its length first is safe).
- **Real Maelstrom runs conform to the models**, checked from each
  node's step log, merged by Lamport clocks:
  - unique-ids: 2841 steps, with partitions;
  - broadcast: 974–1085 steps, 5 nodes, with partitions;
  - g-counter: 1296 steps.

  The broadcast `ReadOwnOnly` mutant is rejected by both Maelstrom and
  the model, at the first read that misses a gossiped value.

## Limits

- **Cost.** A model whose state grows with the run is slow to replay.
  The 2841-step unique-ids run took 2.5 minutes, because `issued` holds
  every id. Bulk checking (Hegel's `Batched` mode) helps many short runs,
  not one long one.
- **Settling under Maelstrom.** Under real Maelstrom nobody records
  `settle`, so `convergedWhenSettled` and `countsEveryAdd` hold
  vacuously there. Maelstrom's own final reads check eventual delivery.
  Safety and every read's value are checked.
- **Kafka and the Sim.** Kafka's model steps include lin-kv's
  operations, which only the Sim's `KV` records. Under Maelstrom, kafka
  is checked by Maelstrom's checker alone.
- **Not modelled.** Challenge 6 (totally-available transactions) is not
  here yet.
