# examples/fleetrollout: a multi-cluster controller, simulated and on kwok

A platform app: a Kubernetes controller, built on
[multicluster-runtime](https://github.com/kubernetes-sigs/multicluster-runtime),
that rolls one Deployment out to a new image across a fleet of member
clusters. At most `maxUnavailable` clusters may be in progress at once. A
cluster is in progress from the moment its spec has the new image until
its status reports that generation fully rolled out.

The same controller (package `rollout`) runs in two environments. Both
produce the same steps, checked by the same model:

| | Simulated (`kubesim` on [`qenv`](../../qenv)) | Real (`cmd/fleet-controller` on [kwok](https://kwok.sigs.k8s.io)) |
|---|---|---|
| Member clusters | controller-runtime fake clients behind interceptors | three kwok clusters: real etcd, kube-apiserver and kube-controller-manager; pods on fake nodes |
| The controller | `Reconcile` every 200 ms, in a crashable process | a multicluster-runtime manager watching the Deployment in every member |
| Status lag | the simulated Deployment controller observes a new generation 0–500 ms late (a schedule choice), then replaces pods one at a time | the real Deployment controller; `minReadySeconds: 2`, one pod at a time |
| Faults | failed requests, updates applied with their response lost, controller crashes | `kill -9` of the controller (`KILL=1`) |
| Patch steps recorded | at the API server, when it applies them | by the controller, when the update succeeds |
| Runs | many seeds per second, or Hegel | about 45 s per rollout |

The model (`model/fleet.qnt`) works on evidence, with Kubernetes'
semantics:
- **`observe`**: a Deployment as read from its API server. Its
  `resourceVersion` orders the evidence for that object, so a stale read
  changes nothing.
- **`patch`**: a spec update the API server applied.
- **Completeness**: a cluster is complete when `observedGeneration >=
  generation` and every replica is updated and available.
- **Properties**: `withinBudget` is checked at every patch. Once the fleet
  is quiet and every member has been read afresh, `rolledOutWhenSettled`
  checks that every cluster is rolled out.

```sh
go test ./...                                                        # in this module: explorations, Hegel (~4 min)
./kwok-demo.sh                                                       # the real controller on three kwok clusters
MUTATION=ignore-observed-generation ./kwok-demo.sh
KILL=1 MUTATION=in-memory-progress ./kwok-demo.sh
TRACE=../../sktrace/testdata/cronjob.sktrace ./kwok-demo.sh          # pods timed by a production trace
```

kwok's binary runtime needs no Docker. It downloads etcd and the
Kubernetes binaries. Install it with
`go install sigs.k8s.io/kwok/cmd/{kwokctl,kwok}@v0.7.0`.

With `TRACE=` the members' pods take as long as a production trace's pods
did. `cmd/kwok-stages` (`KwokStagesFromTrace`) writes kwok Stages from the
trace's pod lifetimes: kwok's default pod stages, with one weighted
pod-ready stage per quantile (`sktrace.Quantiles`, `SPEED` times faster,
default 10), so each is equally likely. These are the readiness options
`Config.PodTimes` gives the simulated members
(`TestRolloutWithMeasuredPodTimes`). A Job's pods run that long once
ready. The clusters are named `fleet-trace-m*`, apart from the default
ones. With the CronJob trace (30 s, 30 s and 45 s, so 3 s to 4.5 s), the
faithful controller conforms and `IgnoreObservedGeneration` still violates
`withinBudget`. `TestKwokStagesFromTrace` loads the stages with kwok's own
config loader and matches pods with its lifecycle package.

## What each bug looks like

| Controller | Simulated | On kwok |
|---|---|---|
| faithful | conforms (30 seeds, with lost responses and crashes) | conforms, including when killed and restarted |
| `IgnoreObservedGeneration`: trusts a status written for the previous generation | **violates** `withinBudget` | **violates** `withinBudget`: m2 patched while m1 had just started |
| `InMemoryProgress`: counts the clusters it patched, in memory | conforms with no faults; **violates** `withinBudget` after a crash, *or after a lost response* | **violates** `withinBudget` when killed after its first patch |

`IgnoreObservedGeneration` is a classic real controller bug. Right after a
patch, the status still describes the old generation, fully rolled out.
On kwok the real controller manager's lag was enough to trigger it on the
first try, just as the simulated lag did.

Under a production workload (`TestRolloutUnderProductionWorkload`):
- SimKube's scaling trace, a real cluster's Deployments scaled over 19
  minutes, is replayed onto the members as another writer
  (`Config.Workload`, `kubesim.Fleet.Replay`, 60 times faster) while the
  rollout runs, through the same faults.
- The faithful controller conforms, and `IgnoreObservedGeneration` still
  violates `withinBudget`, though in fewer runs: the scalings perturb its
  timing.

## Findings along the way

- **The model had no other writers.** Replaying a real workload put
  another writer's updates on the members, and the model had no step for
  them. A real cluster has autoscalers and people with kubectl. `scale` is
  the environment's spec change, recorded at the API server under
  `kubesim.AsWriter`: the generation moves on, and the cluster is
  complete again only when its status catches up.

- **In-memory progress breaks under lost responses, not just restarts.** I
  wrote the mutant expecting only a crash to expose it. The exploration
  found a violation with no crash at all. An update was applied, its
  response was lost, and the controller never counted the cluster as in
  flight. A level-based controller, which re-reads status every time, is
  safe against both.
- **A killed process loses buffered steps.** The first `KILL=1` run hung:
  the demo waited for a `patch` line still in `FileSink`'s 64 KB buffer.
  `kill -9` would have dropped those steps for good, and steps lost at the
  end of a log cannot be detected, because no later sequence number reveals
  the gap. `qobs.FileSink` writes each step through, and the
  controller uses it.
- **Recording points differ by environment.** The simulation records
  patches at the API server, where they take effect. With real clusters
  the controller records them when the update succeeds, so an update
  applied with its response lost would go unrecorded until the next
  observation. The API server's audit log would be the faithful recording
  point; converting it to steps is future work.
- **Cross-cluster order.** Steps are ordered by the single process that
  records them: the controller, then the final verification read. The
  model's verdicts are about the controller's decisions given what it had
  read. That is sound because the controller reads every member fresh,
  from the API server, before deciding.

## Layout

| | |
|---|---|
| `rollout/` | the controller, against the `Clusters` seam (reader and writer per member); mutants; `observe`/`patch` recording |
| `kubesim/` | simulated members: fake clients with generation and conflict semantics, request faults, a Deployment controller with lag |
| `fleet.go` | the simulated fleet: `Run(chooser, config)`, with an optional production workload (`Config.Workload`, a SimKube trace) |
| `cmd/fleet-controller` | the real controller (multicluster-runtime, static `clusters` provider, uncached reads, durable step log); `-verify` for the final reads |
| `kwok/fleet.yaml`, `kwok-demo.sh` | the members' node and Deployment; the end-to-end run |
| `kwokstages.go`, `cmd/kwok-stages` | kwok pod Stages timed by a trace's lifetimes (`TRACE=` in the demo) |
| `model/fleet.qnt`, `fleet.binding.yaml` | the model and its binding (a new controller process is a `restart`) |
