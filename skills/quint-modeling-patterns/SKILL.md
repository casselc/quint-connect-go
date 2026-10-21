---
name: quint-modeling-patterns
description: Modeling patterns for Quint specs that real Go systems (services, queues, distributed nodes) are checked against with quint-connect-go - specifying the requirement rather than one solution, modeling the environment's real guarantees, linearization points, history properties with ghost variables, eventual properties conditioned on a recorded settle, spec-level vs mechanism-level models with a Hegel-tested lemma, and keeping long replays cheap. Use when deciding what a model should allow, how to state a property, why a correct-looking model passed a buggy system (or rejected a correct one), or why validation of long runs is slow.
---

# Modeling patterns for checked systems

The quint-model-for-go skill covers the mechanics (named actions, nondet
names, constants). This skill is about what to put in the model. Every
pattern here came out of the example apps (`examples/glomers`,
`examples/jobqueue`, `examples/kvstore`). Each is stated with the bug it
catches, or the bug that slips through without it.

## 1. Specify the requirement, not your solution

Nondeterminism in a model is permission. Let the model allow every
strategy the requirement allows, and let the code pick one.
- **Broadcast.** `gossip(src, dst, values)` may send *any subset* of what
  `src` knows. Batching, a spanning tree, retries and flooding are all
  behaviours of one model. The model stays valid while the
  implementation is optimised.
- **Unique ids.** `generate(node, id)` with `id` arbitrary, and the
  property `idsUnique`. Any id scheme can be checked against it.

Model your algorithm instead only when you want to check the mechanism
(see 6). A model that mirrors the code tends to share its bugs.

## 2. Model the environment you actually run on

The model must not assume stronger guarantees than the environment gives:
- lost messages are messages never delivered (no `drop` action needed);
- partitions are long runs of non-delivery;
- a store is exactly as consistent as its documentation says.

In examples/glomers, two counter designs passed a model that assumed a
linearizable store, and failed under Maelstrom. Its seq-kv store is only
sequentially consistent, and "update operations may interact with past
states". Either model the weaker guarantee or design so you don't depend
on it (the G-counter CRDT). In both cases, also run against the real
environment. In-process, `qenv`'s doubles enforce the documented
contract: a store at the isolation level production runs, an
at-least-once queue, a network that loses responses. Run each property
under every contract you might deploy on (the quint-fault-injection
skill).

## 3. Record where effects happen: linearization points

A step must be recorded where it takes effect, in the order effects take
place:
- **In-process.** Record inside the critical section that orders the
  effect (kvstore's CAS). Recording after unlocking reorders concurrent
  steps, and correct runs then fail.
- **Shared services.** Record at the service, when it applies the
  operation (glomers `maelstrom.KV`), not at the client when it hears
  back. A client's view comes later and can be reordered.
- **Across nodes.** Use program order per node (`thread`) plus a logical
  clock carried in messages (`order.lamport`). A delivery is then ordered
  after its send.

## 4. History properties: check on arrival, remember in a ghost

"No id is issued twice" and "nothing is delivered twice" are properties of
the whole history. Don't re-scan the history in every state: that is
quadratic over a run. Check each new element as it arrives and latch the
verdict:

```quint
var issued: Set[str]
var repeated: bool          // ghost: no implementation counterpart
action generate(node: str, id: str): bool = all {
  issued' = issued.union(Set(id)),
  repeated' = repeated or issued.contains(id),   // parenthesise: `or` binds loosely
}
val idsUnique = not(repeated)
```

Prefer this to disabling the step (`not(issued.contains(id))` as a
guard). The run still conforms, and the report says *which* property
failed and after which step. A disabled step only says "not a
transition".

## 5. Eventual properties: condition them on a recorded `settle`

Invariants check every state, so "every node eventually has every value"
cannot be an invariant as is. Instead:
- give the model an action `settle`, which sets a `settled` flag;
- have the test record `settle` once the system has quiesced (heal, then
  deliver and tick until quiet: glomers `Sim.Settle`);
- write the property as `val convergedWhenSettled = settled implies ...`.

A fire-and-forget gossip (`NoRetry`) conforms at every step and fails
here, after the partition heals.

Where nobody records `settle` (real Maelstrom), the property holds
vacuously. Say so in the README, and rely on the environment's own final
checks there.

## 6. Spec model vs mechanism model, plus a lemma

When the spec model's state grows with the run (every issued id), a
second model can check *why* the property holds, with bounded state:
- **The mechanism model.** glomers `uniqueidsScheme.qnt` records ids as
  (node, incarnation, counter). It checks that each incarnation belongs
  to one node and that its counter only increases. Its state grows only
  with the number of incarnations.
- **The lemma.** That argument also needs a fact about a pure function:
  the encoding of (node, incarnation, counter) as a string is injective.
  A Hegel property test checks it (`TestIDEncodingInjective`). It found
  both broken encodings, which the mechanism model alone misses.

Use the spec model when the state is affordable, and the mechanism model
for long runs. Never use the mechanism model without its lemma.

## 7. Outcomes, refusals and restarts are part of the model

- **Outcomes.** Model an outcome the design cares about as a parameter
  (`ack(worker, job, o: Outcome)`). Map it in the binding with
  `outcomes: { ok: Ok, stale: Stale }`.
- **Refusals.** If the model has no step for a refused call, the binding
  says `outcomes: { error: skip }`. A refused call is then no step, and a
  wrongly *accepted* one is a step the model rejects. Drive invalid
  inputs (Hegel) to exercise this.
- **Restarts.** A crash is a parameterless model action (`restart`). The
  binding's `restart:` inserts it where the recorded process identity
  changes.

## 8. Design limits: properties you check but don't promise

An at-least-once queue performs a job twice when a lease expires while
its worker is still working. Keep such properties in the model
(`exactlyOnce`) but out of the binding's `invariants`. Check them on
demand (`quintgo validate -invariants exactlyOnce`,
`Checker.WithInvariants`). "Conforms, exactlyOnce FAIL at step [i]" then
says when a real run hit the design's limit.

## 9. Abstraction and cost

- **Opaque ids.** Opaque ids from the implementation (`g20260924T…`) can
  become small integers with `intern:` in the binding. Model constants
  are then sized per run from templates (`{{ set "node" }}`,
  `{{ atLeast 5 (max "amount") }}`).
- **Collections.** Record sets, lists and maps as Go slices and maps. A
  parameter of set, list or map type is converted by default. In
  `expect`, use `toSet`, `toList` or `toMap`.
- **Cost.** Cost per step is roughly the state size times the invariants'
  work. For long runs:
  - prefer ghosts to scans (4) and bounded mechanism state (6);
  - pass `-noitf` when you don't need the ITF of a conforming run.

  Measured on a 2841-step log, the four unique-id models take 64 s, 20 s,
  21 s and 7 s (README of examples/glomers).

## Checklist for a new model

- [ ] Does it allow every strategy the requirement allows? (1)
- [ ] Are the environment's guarantees as weak as documented? (2)
- [ ] Is every step recorded at its linearization point? (3)
- [ ] Is each history property checked on arrival, with a ghost? (4)
- [ ] Is each eventual property conditioned on `settled`, and recorded? (5)
- [ ] Are outcomes, refusals and restarts modelled? (7)
- [ ] Are promised and unpromised properties separated? (8)
- [ ] Is there a mutant per property, showing it can fail? (see quint-verification-workflow)
