// -*- mode: Bluespec; -*-
// A stream pipeline: a publisher writes keyed events (key, n: the key's
// n-th event) to a partitioned log (Kafka topic, Kinesis stream);
// consumers read the log, apply each event to a projection, and commit
// (checkpoint) how far they got.
//
// The model is the requirement, not a broker: it holds no partitioning,
// group protocol or retry policy. What the log holds is learned from the
// consumers' reads (a record at an offset never changes, and a partition
// is read without holes), so the same model checks a run on Kafka, on
// Kinesis or on the simulated log. Duplicates in the log are allowed (an
// at-least-once producer writes them); the properties say what the
// projection must look like anyway:
//   - every event the publisher was told is written is applied exactly
//     once, once the system is quiet (settledCorrectly);
//   - never twice (appliedAtMostOnce);
//   - a key's events are applied in order (perKeyOrder);
//   - no commit is past a record not yet applied (committedApplied): a
//     consumer that crashes after committing must not lose what it
//     committed over.
//
// The last three are ghosts set by the step that breaks them, not
// predicates over the whole log: applies only accumulate, so a commit
// past an unapplied record is wrong when it is made and stays so, and a
// replay of hundreds of steps does not rescan the log at every state.
module streams {
  type Ev = { key: str, n: int }

  var log: int -> List[Ev]      // partition -> records read so far, by offset
  var seen: Set[Ev]             // events some consumer read
  var applied: Ev -> int        // times each event was applied to the projection
  var acked: Set[Ev]            // events the publisher was told are written
  var twice: bool               // ghost: an event applied a second time
  var gap: bool                 // ghost: an event applied before its key's previous one
  var badCommit: bool           // ghost: a commit past an unapplied record
  var settled: bool             // the system has quiesced

  pure def logOf(l: int -> List[Ev], p: int): List[Ev] = if (l.keys().contains(p)) l.get(p) else List()
  pure def count(m: Ev -> int, e: Ev): int = if (m.keys().contains(e)) m.get(e) else 0

  action init = all {
    log' = Map(), seen' = Set(), applied' = Map(), acked' = Set(),
    twice' = false, gap' = false, badCommit' = false, settled' = false,
  }

  // The publisher learned the key's n-th event is in the log.
  action publish(key: str, n: int): bool = all {
    n >= 1,
    acked' = acked.union(Set({ key: key, n: n })),
    log' = log, seen' = seen, applied' = applied, twice' = twice, gap' = gap, badCommit' = badCommit, settled' = settled,
  }

  // A consumer read the record at offset of partition. The first read of
  // an offset learns the record; a later read must find the same one. An
  // offset past the end of what was read would skip records no one read.
  action read(partition: int, offset: int, key: str, n: int): bool = {
    val l = logOf(log, partition)
    val ev = { key: key, n: n }
    all {
      partition >= 0, offset >= 0, offset <= l.length(),
      offset < l.length() implies l[offset] == ev,
      log' = if (offset == l.length()) log.put(partition, l.append(ev)) else log,
      seen' = seen.union(Set(ev)),
      applied' = applied, acked' = acked, twice' = twice, gap' = gap, badCommit' = badCommit, settled' = settled,
    }
  }

  // The projection applied an event some consumer read. Applying it again,
  // or before its key's previous event, is allowed (it happens) but
  // remembered: the invariants are the properties.
  action apply(key: str, n: int): bool = {
    val ev = { key: key, n: n }
    all {
      seen.contains(ev),
      applied' = applied.put(ev, count(applied, ev) + 1),
      twice' = (twice or count(applied, ev) > 0),
      gap' = (gap or (n > 1 and count(applied, { key: key, n: n - 1 }) == 0)),
      log' = log, seen' = seen, acked' = acked, badCommit' = badCommit, settled' = settled,
    }
  }

  // A consumer's commit of partition up to offset took effect: every
  // record before offset was processed. Committing past what was read is
  // not a step.
  action commit(partition: int, offset: int): bool = {
    val l = logOf(log, partition)
    all {
      offset >= 0, offset <= l.length(),
      badCommit' = (badCommit or l.indices().exists(i => i < offset and count(applied, l[i]) == 0)),
      log' = log, seen' = seen, applied' = applied, acked' = acked, twice' = twice, gap' = gap, settled' = settled,
    }
  }

  action settle = all {
    settled' = true,
    log' = log, seen' = seen, applied' = applied, acked' = acked, twice' = twice, gap' = gap, badCommit' = badCommit,
  }

  val appliedAtMostOnce = not(twice)

  val perKeyOrder = not(gap)

  val committedApplied = not(badCommit)

  val settledCorrectly = settled implies acked.forall(e => count(applied, e) == 1)

  // For simulation: a publisher, reads, applies and commits, in any
  // interleaving.
  action step = {
    nondet key = Set("a", "b").oneOf()
    nondet n = 1.to(2).oneOf()
    nondet p = 0.to(1).oneOf()
    nondet o = 0.to(3).oneOf()
    any { publish(key, n), read(p, o, key, n), apply(key, n), commit(p, o), settle }
  }
}
