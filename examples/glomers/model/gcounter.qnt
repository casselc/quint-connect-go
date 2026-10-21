// -*- mode: Bluespec; -*-
// Gossip Glomers challenge 4: a grow-only counter.
// https://fly.io/dist-sys/4/
//
// A G-counter CRDT. Each node counts the adds it acknowledged, and nodes
// gossip vectors of per-node counts, merging by taking the maximum of each
// entry. A read is the sum of what a node knows. The network reorders and
// loses messages; nothing depends on a key-value store's consistency.
//
// (The challenge suggests seq-kv. Two seq-kv designs failed Maelstrom's
// checker with stale final reads. Their models had assumed seq-kv
// stronger than it is: updates may act on past states, not just reads.
// See ../README.md.)
module gcounter {
  const NODES: Set[str]

  type Vec = str -> int
  type Gossip = { src: str, dst: str, vec: Vec }

  var known: str -> Vec       // what each node knows of every node's count
  var inflight: str -> Gossip
  var acked: int              // sum of the adds acknowledged to clients
  var settled: bool
  var lastReads: str -> int   // after settle: what each node's read returned

  pure def at(v: Vec, n: str): int = if (v.keys().contains(n)) v.get(n) else 0
  pure def merge(a: Vec, b: Vec): Vec = a.keys().union(b.keys()).mapBy(n => if (at(a, n) > at(b, n)) at(a, n) else at(b, n))
  pure def total(v: Vec): int = v.keys().fold(0, (s, n) => s + v.get(n))

  action init = all {
    known' = NODES.mapBy(_ => Map()), inflight' = Map(), acked' = 0, settled' = false, lastReads' = Map(),
  }

  // node acknowledged add(delta): it counts it in its own entry.
  action add(node: str, delta: int): bool = all {
    NODES.contains(node), delta >= 0, not(settled),
    known' = known.setBy(node, v => v.put(node, at(v, node) + delta)),
    acked' = acked + delta,
    inflight' = inflight, settled' = settled, lastReads' = lastReads,
  }

  // src sends dst a vector no newer than what it knows.
  action gossip(id: str, src: str, dst: str, vec: Vec): bool = all {
    NODES.contains(src), NODES.contains(dst),
    not(inflight.keys().contains(id)),
    vec.keys().forall(n => vec.get(n) <= at(known.get(src), n)),
    inflight' = inflight.put(id, { src: src, dst: dst, vec: vec }),
    known' = known, acked' = acked, settled' = settled, lastReads' = lastReads,
  }

  action deliver(id: str): bool = all {
    inflight.keys().contains(id),
    known' = known.setBy(inflight.get(id).dst, v => merge(v, inflight.get(id).vec)),
    inflight' = inflight.keys().exclude(Set(id)).mapBy(k => inflight.get(k)),
    acked' = acked, settled' = settled, lastReads' = lastReads,
  }

  // A read returns the sum of what the node knows.
  action read(node: str, value: int): bool = all {
    NODES.contains(node),
    value == total(known.get(node)),
    lastReads' = if (settled) lastReads.put(node, value) else lastReads,
    known' = known, inflight' = inflight, acked' = acked, settled' = settled,
  }

  action settle = all {
    not(settled), settled' = true,
    known' = known, inflight' = inflight, acked' = acked, lastReads' = lastReads,
  }

  // Never more than was added.
  val noOvercount = NODES.forall(n => total(known.get(n)) <= acked)
  // Once quiet, every node counts every add, and its reads say so.
  val countsEveryAdd = settled implies
    (NODES.forall(n => total(known.get(n)) == acked) and lastReads.keys().forall(n => lastReads.get(n) == acked))
}
