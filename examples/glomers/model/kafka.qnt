// -*- mode: Bluespec; -*-
// Gossip Glomers challenge 5: a Kafka-style log, replicated through
// Maelstrom's lin-kv (5b, 5c).
// https://fly.io/dist-sys/5a/
//
// Each key's log is one list in lin-kv, appended by compare-and-swap; a
// message's offset is its position. Committed offsets are kept in lin-kv
// too. The KV operations are recorded where they take effect (the
// service); sends, polls, commits and listings where nodes answer clients.
module kafka {
  const KEYS: Set[str]
  const NODES: Set[str]

  var logs: str -> List[int]    // lin-kv log/<key>
  var commits: str -> int       // lin-kv commit/<key>
  var sent: Set[(str, int, int)] // (key, offset, msg) acknowledged to producers
  var settled: bool

  pure def orEmpty(m: str -> List[int], k: str): List[int] = if (m.keys().contains(k)) m.get(k) else List()
  pure def orNone(m: str -> int, k: str): int = if (m.keys().contains(k)) m.get(k) else -1

  action init = all { logs' = Map(), commits' = Map(), sent' = Set(), settled' = false }

  // --- at lin-kv
  action logRead(node: str, key: str, value: List[int]): bool = all {
    NODES.contains(node), value == orEmpty(logs, key),
    logs' = logs, commits' = commits, sent' = sent, settled' = settled,
  }
  action logCas(node: str, key: str, from: List[int], into: List[int], ok: bool): bool = all {
    NODES.contains(node), KEYS.contains(key),
    if (orEmpty(logs, key) == from) all { ok, logs' = logs.put(key, into) } else all { not(ok), logs' = logs },
    commits' = commits, sent' = sent, settled' = settled,
  }
  action logWrite(node: str, key: str, value: List[int]): bool = all {
    NODES.contains(node), KEYS.contains(key),
    logs' = logs.put(key, value), commits' = commits, sent' = sent, settled' = settled,
  }
  action commitRead(node: str, key: str, value: int): bool = all {
    NODES.contains(node), value == orNone(commits, key),
    logs' = logs, commits' = commits, sent' = sent, settled' = settled,
  }
  action commitCas(node: str, key: str, from: int, into: int, ok: bool): bool = all {
    NODES.contains(node), KEYS.contains(key),
    if (orNone(commits, key) == from) all { ok, commits' = commits.put(key, into) } else all { not(ok), commits' = commits },
    logs' = logs, sent' = sent, settled' = settled,
  }

  // --- at the nodes: answers to clients
  action sendOk(node: str, key: str, msg: int, offset: int): bool = all {
    NODES.contains(node),
    sent' = sent.union(Set((key, offset, msg))),
    logs' = logs, commits' = commits, settled' = settled,
  }
  // poll and list_committed_offsets: what they returned is checked by the binding.
  action pollOk(node: str, key: str): bool = all {
    NODES.contains(node), logs' = logs, commits' = commits, sent' = sent, settled' = settled,
  }
  action listOk(node: str, key: str): bool = all {
    NODES.contains(node), logs' = logs, commits' = commits, sent' = sent, settled' = settled,
  }
  action settle = all { not(settled), settled' = true, logs' = logs, commits' = commits, sent' = sent }

  // An acknowledged send is in the log, at the offset the producer was told.
  val sentAtItsOffset = sent.forall(((k, o, m)) => orEmpty(logs, k).length() > o and orEmpty(logs, k)[o] == m)
  // No two acknowledged sends share an offset.
  val offsetsUnique = sent.forall(((k1, o1, m1)) => sent.forall(((k2, o2, m2)) => (k1 == k2 and o1 == o2) implies m1 == m2))
  // Committed offsets point into the log.
  val commitsInLog = commits.keys().forall(k => commits.get(k) < orEmpty(logs, k).length())
}
