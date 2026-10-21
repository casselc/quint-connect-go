// -*- mode: Bluespec; -*-
// The jobqueue example: an at-least-once job queue with leases.
//
// Workers lease pending jobs, perform them (a side effect, e.g. sending
// an email), and ack them. A lease that is not acked in time expires and
// the job is leased again, to someone else or the same worker; the
// worker holding the expired lease may still be working. The queue
// process can restart: leases are in memory and are lost, while the
// journal keeps which jobs exist and which are done.
module jobqueue {
  const JOBS: Set[int]
  const WORKERS: Set[str]

  type Status = Pending | Leased(str) | Done
  type AckOutcome = Ok | Stale

  var jobs: int -> Status
  // Multisets (how many of each): a worker may hold two leases of one job,
  // when its first expired and it leased the job again, and perform it
  // under each.
  var working: (str, int) -> int   // a worker leased the job and is performing it
  var finished: (str, int) -> int  // a worker performed the job and has not acked it yet
  var performed: int -> int        // how many times each job's side effect happened

  pure def count(m: (str, int) -> int, k: (str, int)): int = if (m.keys().contains(k)) m.get(k) else 0
  pure def add(m: (str, int) -> int, k: (str, int)): (str, int) -> int = m.put(k, count(m, k) + 1)
  pure def take(m: (str, int) -> int, k: (str, int)): (str, int) -> int = m.put(k, count(m, k) - 1)

  action init = all { jobs' = Map(), working' = Map(), finished' = Map(), performed' = Map() }

  action enqueue(job: int): bool = all {
    JOBS.contains(job),
    not(jobs.keys().contains(job)),
    jobs' = jobs.put(job, Pending),
    working' = working,
    finished' = finished,
    performed' = performed.put(job, 0),
  }

  action lease(worker: str, job: int): bool = all {
    WORKERS.contains(worker),
    jobs.keys().contains(job),
    jobs.get(job) == Pending,
    jobs' = jobs.set(job, Leased(worker)),
    working' = working.add((worker, job)),
    finished' = finished,
    performed' = performed,
  }

  // A worker performs a job it leased, whether or not its lease is still valid.
  action perform(worker: str, job: int): bool = all {
    count(working, (worker, job)) > 0,
    jobs' = jobs,
    working' = working.take((worker, job)),
    finished' = finished.add((worker, job)),
    performed' = performed.setBy(job, n => n + 1),
  }

  // A worker acks a job it performed. Only the current lease holder's ack
  // completes it; a stale ack is refused.
  action ack(worker: str, job: int, o: AckOutcome): bool = all {
    count(finished, (worker, job)) > 0,
    if (jobs.get(job) == Leased(worker))
      all { o == Ok, jobs' = jobs.set(job, Done) }
    else
      all { o == Stale, jobs' = jobs },
    working' = working,
    finished' = finished.take((worker, job)),
    performed' = performed,
  }

  action expire(job: int): bool = all {
    jobs.keys().contains(job),
    match jobs.get(job) { Leased(_) => true | _ => false },
    jobs' = jobs.set(job, Pending),
    working' = working,
    finished' = finished,
    performed' = performed,
  }

  // The queue process restarts: in-memory leases are lost; the journal
  // keeps the jobs and which are done. Workers keep working.
  action restart = all {
    jobs' = jobs.keys().mapBy(j => if (jobs.get(j) == Done) Done else Pending),
    working' = working,
    finished' = finished,
    performed' = performed,
  }

  action step = {
    nondet job = JOBS.oneOf()
    nondet worker = WORKERS.oneOf()
    any {
      enqueue(job),
      lease(worker, job),
      perform(worker, job),
      ack(worker, job, if (jobs.keys().contains(job) and jobs.get(job) == Leased(worker)) Ok else Stale),
      expire(job),
      restart,
    }
  }

  // What the design guarantees.
  val doneImpliesPerformed = jobs.keys().forall(j => jobs.get(j) == Done implies performed.get(j) >= 1)

  // What it does NOT guarantee, by design: with leases that expire, a job
  // can be performed twice. Checking it on real runs shows when it happens.
  val exactlyOnce = jobs.keys().forall(j => performed.get(j) <= 1)
}

module jobqueueDesign {
  import jobqueue(JOBS = Set(1, 2, 3), WORKERS = Set("w1", "w2")).*
}
