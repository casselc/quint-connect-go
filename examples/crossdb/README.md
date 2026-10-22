# examples/crossdb: a Crossplane provider, simulated and on kind

A Crossplane provider for a database-as-a-service, built on
crossplane-runtime's managed reconciler:
- a `Database` managed resource (namespaced, Crossplane v2) becomes a
  database in the dbaas cloud;
- its connection details (endpoint, username, password) are written back
  into the cluster as a Secret.

On kind, full Crossplane composes an `AppDatabase` platform API into
`Database`s.

The reconciler under test is **Crossplane's own**. This example supplies
the external client, configures the reconciler, and runs it in two
environments, checked by one model:

| | Simulated (`crossdb.Run`, on [`qenv`](../../qenv)) | Real (`kind/demo.sh`) |
|---|---|---|
| Kubernetes API | controller-runtime fake client behind interceptors: requests fail, or are applied with their response lost | kind, with Crossplane 2.4 and function-patch-and-transform |
| The cloud | `dbaas.Cloud` behind `qenv.Service`: lost or duplicated requests, lost responses, throttling, reads that lag creates | the same `Cloud` behind `cmd/dbaas-server`: dropped responses (`DROP`), throttling, reads that lag creates |
| The provider | the managed reconciler in a crashable process | `cmd/provider` out of cluster; `KILL=1` sends `kill -9` every 2–6 s |
| Users | clients create `Database`s, delete some | `cmd/driver` creates `AppDatabase`s (composed into `Database`s), deletes some |
| Steps | the cloud records its own operations; the clients record theirs; an audit after quiescence records each resource's phase and Secret | the same, from two durable step logs merged by wall clock |

The model (`model/crossdb.qnt`) has two properties:
- **`oneDatabasePerResource`:** a ghost flag, set when a resource gets a
  second live database.
- **`settledCorrectly`:** once quiet,
  - deletions have finished and leaked nothing;
  - every live resource has its one database and a Secret that opens it
    (the endpoint matches, and the password's fingerprint matches the
    cloud's).

```sh
go test ./...                                        # explorations and Hegel (~4 min)
kind/demo.sh                                         # kind + Crossplane; exit status is the verdict
KILL=1 kind/demo.sh                                  # kill -9 the provider every few seconds
MUTATION=no-client-token DROP=0.3 N=6 kind/demo.sh
```

`kind/setup.sh` creates the cluster and installs everything. It needs
Docker, kind, kubectl and helm. In sandboxed VMs:
- set `CA_BUNDLE=` for a TLS-intercepting proxy;
- the script already handles two host quirks, documented in it.

## The design, and what each mistake does

A Crossplane provider for a resource whose ID the cloud generates must
answer one question: what if the provider doesn't learn the outcome of a
create? A response can be lost, or the provider can crash before saving
the ID. The faithful provider's answer:
- **A deterministic external name.** The external name is a client token
  derived from the resource's UID, set before anything is created.
- **An idempotent create.** `Create` sends that token, so retrying a
  create is safe (`WithDeterministicExternalName(true)`), and `Observe`
  finds the database by it.
- **The password first.** The admin password goes into a Secret before the
  create. Every `Observe` publishes it with the endpoint, and `Update`
  resets a cloud password that differs.

| Provider | Simulated | On kind |
|---|---|---|
| faithful | conforms (60 seeds: every fault, crashes) | conforms, with 10% of responses dropped and seven `kill -9`s of the provider |
| `GeneratedExternalName`: the cloud's ID as the external name, with a client token | conforms with no faults; **violates** `settledCorrectly` with lost responses alone, and with crashes alone | conforms, in a 6-resource run with 30% of responses dropped and in one with 14 `kill -9`s: the windows are narrow (see below) |
| `NoClientToken`: the same, without the token | **violates** `oneDatabasePerResource`: a lost response, retried as a failure, makes a second database | **violates** `oneDatabasePerResource` (30% dropped): `app1` got a second database |
| `PasswordFromCreate`: the password generated in `Create`, published only with its result | **violates** `settledCorrectly`: after a lost response, the retry's new password is ignored by the idempotent cloud, and the Secret doesn't open the database | **violates** `settledCorrectly` (30% dropped) |

## Findings along the way

- **A leak with no crash.** With the cloud's ID as the external name, a
  create whose response is lost is marked failed. That's Crossplane's
  contract: an error from `Create` means failure. If the user deletes the
  resource before the retry, there is no name to look up, so the finalizer
  comes off and the database leaks. Crossplane's create-pending
  annotations only protect against crashes, and a crash is worse: the
  reconciler refuses to continue. A deterministic external name closes
  both holes.
- **The creation grace period is not optional.** I first set it to 0,
  reasoning that idempotent creates don't need it. The exploration then
  found a leak:
  1. a resource is deleted right after its database is created;
  2. the cloud's reads still lag the create, so `Observe` sees nothing;
  3. the finalizer comes off, and the database leaks.

  The grace period is what protects deletion from an eventually consistent
  cloud. The cost is that deleting a just-created resource takes the full
  30 s.
- **The grace period counts from the wrong annotation.** crossplane-runtime
  starts it at create-succeeded. A create that timed out is recorded as
  failed, though the cloud may have made the database. Deleted within the
  cloud's read lag, the resource's `Observe` saw nothing, the finalizer
  came off, and the database leaked. Seed 50 found it once qenv's
  services let a crash fall after the effect by default. `Observe`
  now waits out the grace period from create-pending while the resource
  is being deleted, whatever result was recorded.
- **What only the simulation found.** On kind, the generated-external-name
  mutant passed both runs. Its leak needs a create whose response is lost
  *and* a delete before the retry. Its stuck state needs a `kill -9` in
  the few milliseconds between the create and saving the name. A handful
  of real runs didn't produce either coincidence. Sixty seeded simulations
  did, and Hegel can shrink one. This is the case for the simulated tier:
  real runs confirm what the simulation finds, but they rarely reach
  windows this narrow.
- **A library that reads the real clock.** crossplane-runtime stamps its
  create annotations with `time.Now()` and compares them with
  `time.Since`. A simulation advances only virtual time, so the grace
  period never expired and no deletion ever finished. The kube interceptor
  now translates those annotations at the seam: they're stored in virtual
  time (`Sim.ToVirtual`) and handed back in real time (`Sim.ToReal`). The
  first version translated Update but not Patch, which double-translated
  the timestamps. The tests caught it as dates in 2025.
- **One thread per goroutine.** The driver first recorded every
  goroutine's steps as one thread, so wall-clock order contradicted
  program order, and validation reported a cycle.
- **Running kind in a sandboxed VM** needed two fixes, now in
  `kind/setup.sh`. The host forbids lowering `oom_score_adj`, which made
  runc fail every control-plane pod with "can't get final child's PID from
  pipe"; containerd's `restrict_oom_score_adj` fixes that. kind also
  copied a localhost proxy into the node, so the cluster is created
  without the proxy variables.

## Under load: the requeue policy (`load.go`, `metastable_test.go`)

The checks above concern correctness: what each reconcile does. A
separate question is whether the provider and the cloud recover together
from an incident. In the scenario, the cloud API slows down for a
minute, the provider's calls time out, and its requeues might keep the
API saturated after the slowdown ends. This is the performance tier, on
[`qload`](../../qload) and [`qmeta`](../../qmeta). It is an abstraction:
- each Database is a user of a closed loop, observed a poll interval
  after its last reconcile, or requeued after a delay if it failed;
- the cloud API is a queued server.

The abstraction keeps what decides a retry storm: what is in flight, how
soon a failure comes back, and the rate limits. It drops the rest. The
requeue is crossplane-runtime's own limiter logic on virtual time:
per-item exponential backoff, `x/time/rate` for the global limit.

The setup is 600 Databases polled every minute. The cloud API serves 20
calls/s, and the provider times out after 2 s. The cloud slows to a
third for a minute.

| Requeue policy | Runs that recover (4 seeds) | CTMC recovery from a full queue | Hegel's smallest breaking dip (≤4x, ≤5 min) |
|---|---|---|---|
| crossplane-runtime defaults: per-item 1 s→60 s, global 10 reconciles/s | 4/4 | 9 s | none in 25 cases |
| no global limit (`--max-reconcile-rate` out of the way) | 4/4 | 10 s | |
| fixed 1 s requeue on error (`RequeueAfter: time.Second`, a hand-written controller) | 0/4 | 3·10¹⁴ s | 1.6x slower for 2m21s |
| fixed 1 s requeue, with the global limit | 4/4 | 9 s | |

With a fixed requeue, each failing object returns every 3 s (timeout
plus requeue): 200 calls/s against 20. Either half of crossplane-runtime's
defaults prevents the storm on its own. The per-item backoff's 60 s cap
is about the poll interval, so a failing object costs no more than a
healthy one. The global limit caps the provider's total. An open-loop
version of this abstraction counted a failing object's polls on top of
its retries, and found storms under the defaults that the workqueue makes
impossible. Model controllers as closed loops.

```sh
go test -run ControlPlane -v .     # the table; the trigger search is skipped under -short
```

## Layout

| | |
|---|---|
| `apis/v1alpha1` | the `Database` managed resource; `package/crds` (controller-gen) |
| `dbaas/` | the cloud: `Cloud` (contract, step recording), HTTP server and client, simulated client |
| `provider/` | the external client and reconciler options; mutants |
| `crossdb.go` | the simulated deployment: fake kube API with faults and clock translation, provider process, clients, audit |
| `load.go` | the control plane under load: the provider's requeue policies as a `qload` system and a `qmeta` chain |
| `model/crossdb.qnt`, `crossdb.binding.yaml` | the model and its binding |
| `cmd/provider`, `cmd/dbaas-server`, `cmd/driver` | the real environment's processes |
| `kind/` | `setup.sh` (kind + Crossplane), `platform.yaml` (XRD, Composition, RBAC), `demo.sh` |
