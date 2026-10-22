// A counter service that applies each request exactly once. The network
// may deliver a request twice (a retry, a duplicate), so the service sees
// some requests again: it must recognise them (replay) and not count them.
module hello {
  // the requests the clients send, by id
  const REQUESTS: Set[int]

  var applied: Set[int]
  var count: int

  action init = all { applied' = Set(), count' = 0 }

  // a request seen for the first time is counted
  action apply(id: int): bool = all {
    REQUESTS.contains(id),
    not(applied.contains(id)),
    applied' = applied.union(Set(id)),
    count' = count + 1,
  }

  // a request seen again changes nothing
  action replay(id: int): bool = all {
    applied.contains(id),
    applied' = applied,
    count' = count,
  }

  action step = { nondet id = REQUESTS.oneOf() any { apply(id), replay(id) } }

  val exactlyOnce = count == applied.size()
}
