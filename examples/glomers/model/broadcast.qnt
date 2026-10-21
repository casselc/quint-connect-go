// -*- mode: Bluespec; -*-
// Gossip Glomers challenge 3: broadcast (3a-3e).
// https://fly.io/dist-sys/3a/
//
// Clients broadcast values to nodes; nodes gossip what they know; reads
// return what a node knows. The network reorders and loses messages and
// can be partitioned. The model is deliberately abstract about the gossip
// strategy: any node may send any subset of what it knows to any node at
// any time, so a flood, a spanning tree, batching or retries are all its
// behaviours. A lost message is one that is never delivered.
//
// Safety: nodes only know broadcast values. Eventual delivery, the
// challenge's real requirement, as a checkable property: once the test
// harness has healed the network and let the system quiesce (`settle`),
// every node knows every value.
module broadcast {
  const NODES: Set[str]
  const VALUES: Set[int]

  type Gossip = { src: str, dst: str, values: Set[int] }

  var seen: str -> Set[int]
  var broadcasted: Set[int]
  var inflight: str -> Gossip  // sent, not delivered (yet, or ever)
  var settled: bool

  action init = all {
    seen' = NODES.mapBy(_ => Set()),
    broadcasted' = Set(),
    inflight' = Map(),
    settled' = false,
  }

  // A client asked node to broadcast value; the node acknowledged it.
  action broadcast(node: str, value: int): bool = all {
    NODES.contains(node),
    not(settled),
    seen' = seen.setBy(node, s => s.union(Set(value))),
    broadcasted' = broadcasted.union(Set(value)),
    inflight' = inflight,
    settled' = settled,
  }

  // src sends dst some of what it knows, as message id.
  action gossip(id: str, src: str, dst: str, values: Set[int]): bool = all {
    NODES.contains(src),
    NODES.contains(dst),
    not(inflight.keys().contains(id)),
    values.subseteq(seen.get(src)),
    inflight' = inflight.put(id, { src: src, dst: dst, values: values }),
    seen' = seen,
    broadcasted' = broadcasted,
    settled' = settled,
  }

  // Message id arrives.
  action deliver(id: str): bool = all {
    inflight.keys().contains(id),
    seen' = seen.setBy(inflight.get(id).dst, s => s.union(inflight.get(id).values)),
    inflight' = inflight.keys().exclude(Set(id)).mapBy(k => inflight.get(k)),
    broadcasted' = broadcasted,
    settled' = settled,
  }

  // A read changes nothing; what it returned is checked against seen.
  action read(node: str): bool = all {
    NODES.contains(node),
    seen' = seen, broadcasted' = broadcasted, inflight' = inflight, settled' = settled,
  }

  // The harness healed the network and let the system quiesce.
  action settle = all {
    not(settled),
    settled' = true,
    seen' = seen, broadcasted' = broadcasted, inflight' = inflight,
  }

  action deliverAny = all {
    inflight.keys().size() > 0,
    nondet id = inflight.keys().oneOf()
    deliver(id),
  }

  action step = {
    nondet node = NODES.oneOf()
    nondet src = NODES.oneOf()
    nondet dst = NODES.oneOf()
    nondet value = VALUES.oneOf()
    nondet values = seen.get(src).powerset().oneOf()
    nondet id = Set("m1", "m2", "m3", "m4").oneOf()
    any {
      broadcast(node, value),
      gossip(id, src, dst, values),
      deliverAny,
      read(node),
    }
  }

  val noInvention = NODES.forall(n => seen.get(n).subseteq(broadcasted))
  val convergedWhenSettled = settled implies NODES.forall(n => seen.get(n) == broadcasted)
}

module broadcastDesign {
  import broadcast(NODES = Set("n1", "n2", "n3"), VALUES = Set(1, 2, 3)).*
}
