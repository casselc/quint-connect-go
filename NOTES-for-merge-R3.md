# R3 merge notes: kwok Stages from a trace's lifetimes

Built in examples/fleetrollout (`kwokstages.go`, `cmd/kwok-stages`,
`kwokstages_test.go`, `kwok-demo.sh` `TRACE=`/`SPEED=`, README) and CI's
fleetrollout kwok step. sktrace is unchanged and still has no dependencies.
fleetrollout's go.mod gains `sigs.k8s.io/kwok v0.7.0`, so the test can load the
stages with kwok's own loader. The k8s.io versions are unchanged.

## README.md (example table, fleetrollout row)

Append to the row: "on kwok, pods can be timed by a production trace
(`TRACE=` in kwok-demo.sh: kwok Stages from its pod lifetimes)".

## AGENTS.md

Layout line for examples/fleetrollout:

```
examples/fleetrollout separate module: multicluster-runtime controller, simulated (kubesim) and on kwok (kwok-demo.sh;
                      TRACE= times pods by a SimKube trace through kwok Stages, cmd/kwok-stages)
```

Gotcha:

- kwok's pod stages given in a cluster's config replace all of its default
  pod stages, but not its node stages. `KwokStages` therefore carries
  kwok's fast pod-ready, pod-complete and pod-delete stages with only the
  delays added. A choice among options is one stage per option with
  weight 1: kwok picks among matching stages by weight. A single stage's
  jitter would be uniform over a range, not the trace's quantiles. Trace
  clusters are named `fleet-trace-m*`, so `KEEP=1` never reuses a cluster
  that has kwok's immediate stages. The demo checks that the cluster's
  kwok.yaml holds the stages.

## docs/DESIGN.md (the SimKube / sktrace section, after D3's PodTimes)

> The kwok tier is timed by the same numbers. `fleetrollout.KwokStages`
> turns `sktrace.Quantiles(tr.Lifetimes(gvk), n)` into kwok pod Stages,
> one weighted pod-ready stage per option. This is the discrete, uniform
> choice kubesim makes with `sched.kube.pod`. A real cluster's pods then
> become ready as slowly as the trace's did. The generator lives in the
> example, not in sktrace: sktrace stays a dependency-free decoder, and
> the stages' templates are kwok's (v0.7.0's
> kustomize/stage/pod/fast), copied unchanged.

## skills

quint-fault-injection, the paragraph on kwok and real environments (or its
references/ file on real tiers):

> To give a kwok cluster the timing of production, generate pod Stages
> from a SimKube trace (`go run ./cmd/kwok-stages -trace f.sktrace -speed
> 10 > stages.yaml`, then `kwokctl create cluster --config stages.yaml`;
> examples/fleetrollout's `TRACE=`). Use the same quantiles as the
> simulated `PodTimes`, so the simulated tier and the kwok tier are timed
> alike.

## docs/ROADMAP.md

Mark R3 done:

> R3 ✓ kwok Stages from a trace's lifetimes. These are
> fleetrollout.KwokStages/KwokStagesFromTrace and cmd/kwok-stages, with
> kwok-demo.sh `TRACE=`. In CI, the faithful run conforms and
> ignore-observed-generation is caught. The stages are loaded and matched
> by kwok's own config and lifecycle packages in a test.
