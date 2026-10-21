// -*- mode: Bluespec; -*-
// Databases managed through Kubernetes: a client creates Database resources
// and deletes some; a provider makes the cloud's databases follow them, and
// writes each resource's connection details (endpoint, password) into a
// Secret.
//
// The model is the requirement. The cloud records what it does (a database
// created for an owner, a password set, a database deleted); the client
// records what it asks for; an audit after quiescence records each
// resource's phase and connection secret, as the API server shows them.
// Passwords appear as fingerprints.
module crossdb {
  type Db = { owner: str, endpoint: str, password: str, deleted: bool }
  type Conn = { endpoint: str, password: str }

  var state: str -> str      // resource -> live | deleting | gone
  var dbs: str -> Db         // cloud database id -> database
  var secrets: str -> Conn   // resource -> its connection secret (audited)
  var duplicate: bool        // ghost: a resource had two live databases
  var settled: bool

  def liveDbsOf(r: str): Set[str] = dbs.keys().filter(id => dbs.get(id).owner == r and not(dbs.get(id).deleted))

  action init = all {
    state' = Map(), dbs' = Map(), secrets' = Map(), duplicate' = false, settled' = false,
  }

  action create(resource: str): bool = all {
    not(state.keys().contains(resource)),
    state' = state.put(resource, "live"),
    dbs' = dbs, secrets' = secrets, duplicate' = duplicate, settled' = settled,
  }

  action delete(resource: str): bool = all {
    state.keys().contains(resource), state.get(resource) == "live",
    state' = state.put(resource, "deleting"),
    dbs' = dbs, secrets' = secrets, duplicate' = duplicate, settled' = settled,
  }

  // The cloud creates a database for a resource that is not gone. (The
  // client may record the resource's creation after the cloud's create: in
  // the real environment it records it when it first sees the resource.) A
  // second live database for the same resource is allowed (it happens) and
  // remembered.
  action dbCreated(id: str, owner: str, endpoint: str, password: str): bool = all {
    not(dbs.keys().contains(id)),
    not(state.keys().contains(owner)) or state.get(owner) != "gone",
    duplicate' = (duplicate or liveDbsOf(owner).size() > 0),
    dbs' = dbs.put(id, { owner: owner, endpoint: endpoint, password: password, deleted: false }),
    state' = state, secrets' = secrets, settled' = settled,
  }

  action dbPassword(id: str, password: str): bool = all {
    dbs.keys().contains(id), not(dbs.get(id).deleted),
    dbs' = dbs.setBy(id, d => { ...d, password: password }),
    state' = state, secrets' = secrets, duplicate' = duplicate, settled' = settled,
  }

  action dbDeleted(id: str): bool = all {
    dbs.keys().contains(id), not(dbs.get(id).deleted),
    dbs' = dbs.setBy(id, d => { ...d, deleted: true }),
    state' = state, secrets' = secrets, duplicate' = duplicate, settled' = settled,
  }

  // The audit: a resource nobody deleted is still there; a deleted one is
  // deleting (its finalizer still holds it) or gone.
  action audit(resource: str, phase: str): bool = all {
    state.keys().contains(resource),
    if (state.get(resource) == "live") phase == "live" else (phase == "deleting" or phase == "gone"),
    state' = state.put(resource, phase),
    dbs' = dbs, secrets' = secrets, duplicate' = duplicate, settled' = settled,
  }

  action secret(resource: str, endpoint: str, password: str): bool = all {
    state.keys().contains(resource), state.get(resource) == "live",
    secrets' = secrets.put(resource, { endpoint: endpoint, password: password }),
    state' = state, dbs' = dbs, duplicate' = duplicate, settled' = settled,
  }

  // A recording process changed (a provider restart, or the audit): the
  // model's state is unaffected.
  action restart = all {
    state' = state, dbs' = dbs, secrets' = secrets, duplicate' = duplicate, settled' = settled,
  }

  action settle = all {
    settled' = true,
    state' = state, dbs' = dbs, secrets' = secrets, duplicate' = duplicate,
  }

  val oneDatabasePerResource = not(duplicate)

  // Once quiet: deletions have finished and leaked nothing, and every live
  // resource has its one database and a secret that opens it.
  val settledCorrectly = settled implies and {
    state.keys().forall(r => state.get(r) != "deleting"),
    dbs.keys().forall(id => dbs.get(id).deleted or state.get(dbs.get(id).owner) == "live"),
    state.keys().filter(r => state.get(r) == "live").forall(r => and {
      liveDbsOf(r).size() == 1,
      secrets.keys().contains(r),
      liveDbsOf(r).forall(id => secrets.get(r) == { endpoint: dbs.get(id).endpoint, password: dbs.get(id).password }),
    }),
  }

  // For simulation: any order of the cloud's and the client's steps.
  action step = {
    nondet resource = Set("default/a", "default/b").oneOf()
    nondet id = Set("db-1", "db-2", "db-3").oneOf()
    nondet password = Set("p1", "p2").oneOf()
    any {
      create(resource), delete(resource), dbCreated(id, resource, id, password),
      dbPassword(id, password), dbDeleted(id),
    }
  }
}
