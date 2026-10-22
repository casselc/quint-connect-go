// -*- mode: Bluespec; -*-
// A rollout across a fleet of clusters: one Deployment moves to TARGET in
// every cluster, with at most MAX_UNAVAILABLE clusters in progress at once.
//
// The model works on evidence, with Kubernetes' semantics:
//   - observe: a Deployment as read from its cluster's API server. Its
//     resourceVersion (rv) orders evidence per object: an older read (a lagging
//     cache, a slow response) changes nothing.
//   - patch: a spec update the API server applied.
//   - a cluster is complete when its status describes its current
//     generation (observedGeneration >= generation) with every replica
//     updated and available; it is in progress when its spec has TARGET
//     and it is not complete.
//
// withinBudget is checked at every patch, against the newest evidence.
module fleet {
  const CLUSTERS: Set[str]
  const TARGET: str
  const MAX_UNAVAILABLE: int

  var lastRV: str -> int       // resourceVersion of the newest evidence, per cluster
  var spec: str -> str
  var gen: str -> int
  var complete: str -> bool
  var overBudget: bool         // ghost: a patch exceeded MAX_UNAVAILABLE
  var settled: bool

  def known = lastRV.keys()

  // clusters in progress, given specs and completeness
  pure def inProgress(sp: str -> str, comp: str -> bool, target: str): Set[str] =
    sp.keys().filter(c => sp.get(c) == target and not(comp.get(c)))

  action init = all {
    lastRV' = Map(), spec' = Map(), gen' = Map(), complete' = Map(),
    overBudget' = false, settled' = false,
  }

  pure def rolledOut(g: int, observedGeneration: int, updated: int, available: int, replicas: int, current: int): bool =
    observedGeneration >= g and updated == replicas and available == replicas and current == replicas

  action observe(cluster: str, rv: int, image: str, generation: int, observedGeneration: int,
                 updated: int, available: int, replicas: int, current: int): bool = all {
    CLUSTERS.contains(cluster),
    if (known.contains(cluster) and rv <= lastRV.get(cluster)) all {
      // stale or repeated evidence: nothing new
      lastRV' = lastRV, spec' = spec, gen' = gen, complete' = complete,
    } else all {
      // newer evidence of the same object: gen never goes back
      not(known.contains(cluster)) or generation >= gen.get(cluster),
      lastRV' = lastRV.put(cluster, rv), spec' = spec.put(cluster, image),
      gen' = gen.put(cluster, generation),
      complete' = complete.put(cluster, rolledOut(generation, observedGeneration, updated, available, replicas, current)),
    },
    overBudget' = overBudget, settled' = settled,
  }

  // The rollout only ever sets TARGET, on a cluster it has read.
  action patch(cluster: str, image: str, rv: int, generation: int): bool = all {
    known.contains(cluster), image == TARGET,
    rv > lastRV.get(cluster), generation >= gen.get(cluster),
    lastRV' = lastRV.put(cluster, rv), spec' = spec.put(cluster, image),
    gen' = gen.put(cluster, generation),
    // a spec change starts a rollout; an unchanged spec keeps the status
    val started = generation > gen.get(cluster)
    val after = if (started) complete.put(cluster, false) else complete
    all {
      complete' = after,
      overBudget' = (overBudget or inProgress(spec.put(cluster, image), after, TARGET).size() > MAX_UNAVAILABLE),
    },
    settled' = settled,
  }

  // Another writer's update (an autoscaler's, a person's): the spec
  // changes outside the rollout, so its generation moves on, and the
  // cluster is complete again only when its status has caught up. A real
  // workload replayed against the fleet (kubesim.Replay) has them, and the
  // model had no step for them.
  // On a cluster not yet read it changes nothing: the next read carries it.
  action scale(cluster: str, rv: int, generation: int, writer: str): bool = all {
    CLUSTERS.contains(cluster),
    if (known.contains(cluster)) all {
      rv > lastRV.get(cluster), generation >= gen.get(cluster),
      lastRV' = lastRV.put(cluster, rv),
      gen' = gen.put(cluster, generation),
      complete' = if (generation > gen.get(cluster)) complete.put(cluster, false) else complete,
    } else all {
      lastRV' = lastRV, gen' = gen, complete' = complete,
    },
    spec' = spec, overBudget' = overBudget, settled' = settled,
  }

  // A controller restart: it forgets nothing the model knows.
  action restart = all {
    lastRV' = lastRV, spec' = spec, gen' = gen, complete' = complete, overBudget' = overBudget, settled' = settled,
  }

  action settle = all {
    settled' = true,
    lastRV' = lastRV, spec' = spec, gen' = gen, complete' = complete, overBudget' = overBudget,
  }

  val withinBudget = not(overBudget)
  // Once quiet (and every cluster read afresh), every cluster is rolled out.
  val rolledOutWhenSettled = settled implies CLUSTERS.forall(c => known.contains(c) and spec.get(c) == TARGET and complete.get(c))
}
