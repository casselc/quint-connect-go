// -*- mode: Bluespec; -*-
// Gossip Glomers challenge 2: unique ID generation.
// https://fly.io/dist-sys/2/
//
// Nodes answer `generate` requests with an id, totally available: no node
// waits for another, even when the network is partitioned. The model lets
// a node answer with any id; uniqueness is the property, so a scheme that
// can repeat an id is a behaviour the model allows and its invariant
// rejects (rather than a step the model forbids).
module uniqueids {
  const NODES: Set[str]

  var issued: str -> int  // how many times each id was given out

  action init = issued' = Map()

  action generate(node: str, id: str): bool = all {
    NODES.contains(node),
    issued' = if (issued.keys().contains(id)) issued.setBy(id, n => n + 1) else issued.put(id, 1),
  }

  // For simulation: ids from a small pool, to show the property can fail.
  action step = {
    nondet node = NODES.oneOf()
    nondet id = Set("a", "b", "c").oneOf()
    generate(node, id)
  }

  val idsUnique = issued.keys().forall(i => issued.get(i) == 1)
}
