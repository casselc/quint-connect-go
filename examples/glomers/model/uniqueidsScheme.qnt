// -*- mode: Bluespec; -*-
// Gossip Glomers challenge 2 at the level of the id scheme.
//
// uniqueids.qnt checks uniqueness of opaque ids, so it must remember every
// id. This model instead checks why the ids are unique. An id is built
// from (node, incarnation, counter), and it is unique if:
//   (1) each incarnation belongs to one node;
//   (2) within an incarnation, the counter only increases;
//   (3) the encoding of (node, incarnation, counter) as an id is injective.
// The model checks (1) and (2) on recorded runs, with state that grows with
// the number of incarnations, not ids. (3) is a property of a pure
// function, checked by a Hegel property test in Go (uniqueids_test.go).
// The three together imply uniqueness; the model alone does not.
module uniqueidsScheme {
  const NODES: Set[str]

  var last: str -> int     // incarnation -> its last counter
  var owner: str -> str    // incarnation -> its node
  var broken: bool         // ghost: (1) or (2) failed

  action init = all { last' = Map(), owner' = Map(), broken' = false }

  action generate(node: str, incarnation: str, counter: int): bool = all {
    NODES.contains(node),
    broken' = (broken or (last.keys().contains(incarnation) and
                          (owner.get(incarnation) != node or counter <= last.get(incarnation)))),
    last' = last.put(incarnation, counter),
    owner' = owner.put(incarnation, node),
  }

  val incarnationsOwnedAndCounting = not(broken)
}
