// -*- mode: Bluespec; -*-
// Gossip Glomers challenge 2: unique ID generation.
// https://fly.io/dist-sys/2/
//
// Nodes answer `generate` requests with an id, totally available: no node
// waits for another, even when the network is partitioned. The model lets
// a node answer with any id; uniqueness is the property, so a scheme that
// can repeat an id is a behaviour the model allows and its invariant
// rejects (rather than a step the model forbids).
//
// Uniqueness is a property of the whole history, and ids are opaque here,
// so the model has to remember every id it has seen. It checks each new id
// against them as it arrives (a ghost flag records a repeat), instead of
// re-checking every id in every state: constant work per step, not
// linear. uniqueidsScheme.qnt avoids remembering ids altogether, by
// modelling how the ids are built.
module uniqueids {
  const NODES: Set[str]

  var issued: Set[str]   // ids given out so far
  var repeated: bool     // ghost: some id was given out twice

  action init = all { issued' = Set(), repeated' = false }

  action generate(node: str, id: str): bool = all {
    NODES.contains(node),
    repeated' = (repeated or issued.contains(id)),
    issued' = issued.union(Set(id)),
  }

  // For simulation: ids from a small pool, to show the property can fail.
  action step = {
    nondet node = NODES.oneOf()
    nondet id = Set("a", "b", "c").oneOf()
    generate(node, id)
  }

  val idsUnique = not(repeated)
}
