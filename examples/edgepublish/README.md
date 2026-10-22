# examples/edgepublish

The case the toolkit was built for. `publisher/` is a stand-in for the chdb
OTel exporter's publish path (casselc/observability,
`otel-chdb/chdbexporter/publish.go`), with the same control flow:
- a generation lock and rotation;
- three S3 writes per push, any of which may fail or fail ambiguously;
- a background seal, crashes and new epochs.

`model/edgePublish.qnt` is its protocol model, vendored from
`otel-chdb/model` together with `basicSpells.qnt`.

This directory is its own Go module, because it depends on
[Orchestrion](https://github.com/DataDog/orchestrion), which weaves the
`//quint:action` annotations in `publisher/`. A plain `go build` ignores
them.

```
binding.yaml       the whole edgePublish mapping: no Go code
publisher/         the stand-in, annotated with //quint:action
scenarios/         demo scenarios, and a test that every trace path gives the same verdict
cmd/demo/          a scenario -> OTLP/JSON spans, native step log, model-level ITF
mbt/               model traces drive the publisher (effects gated so the model schedules them);
                   round trip back through the validator
sim/               the publisher, unchanged, in a qenv simulation (qenv/weave): exploration,
                   mutants, Hegel shrinking, and the load tier
run-demo.sh        everything above, from the CLI
```

## Reproduce

```sh
cd examples/edgepublish
OUT=/tmp/quintgo-demo ./run-demo.sh                                  # 5 scenarios x 3 trace paths
go test ./mbt/ -v                                                    # model traces drive Go
go run github.com/DataDog/orchestrion go test ./mbt/ -run RoundTrip -v
go run github.com/DataDog/orchestrion go test ./scenarios/ -v        # direct emission, both checks, replay
```

Step by step, what `run-demo.sh` does:

```sh
(cd ../.. && go build -o /tmp/q/quintgo ./cmd/quintgo)
go run github.com/DataDog/orchestrion go build -o /tmp/q/demo ./cmd/demo   # weaves //quint:action
/tmp/q/quintgo lint -binding binding.yaml -src publisher
/tmp/q/demo -scenario happy -out /tmp/q/spans.jsonl -steps /tmp/q/steps.jsonl -itf /tmp/q/itf -binding binding.yaml
/tmp/q/quintgo steps /tmp/q/steps.jsonl                                    # the reconstructed order (any format)
/tmp/q/quintgo validate -binding binding.yaml /tmp/q/spans.jsonl   # OTel path
/tmp/q/quintgo validate -binding binding.yaml /tmp/q/steps.jsonl   # native step log
/tmp/q/quintgo validate /tmp/q/itf/edge-1_traces.itf.json          # model-level ITF: no binding needed
/tmp/q/quintgo itf -binding binding.yaml -out /tmp/q/itf2 /tmp/q/spans.jsonl           # any recorded steps -> model-level ITF
```

Scenarios and actual results (`validate` exits 0 or 1):

| scenario | what happens | result |
|---|---|---|
| `happy` | 3 concurrent pushes (steps interleave), a failed Parquet write and its retry, rotation, background seal, crash, new epoch, close | **PASS**, 28 steps (27 observed + inferred `crash`) |
| `rotation-race` | a push is held in its table insert while the clock moves on; the next push must wait for rotation | **PASS** |
| `ambiguous-seal` | a manifest PUT lands but reports failure; the retry commits again; the seal counts 1 of 2 | conforms, but `sealMatchesManifests` fails after step 16 and `committedNoDuplicatePayload` after step 8: the model's known finding F1/F3, now seen in running code |
| `manifest-first` | MUTANT: the manifest is written before the table insert | **FAIL** at step 2: `writeManifest(px(1, 1, 1, 1, ParquetDone), Ok)` is not a transition (QNT513) |
| `no-lock` | MUTANT: the push drops the shared lock before its writes | **FAIL** at step 2: `rotateGen` is not enabled while a push is in flight; `quintgo steps` shows batch 1 committed into generation 1 after its `sealGen` |

```
=== scenario manifest-first
actor edge-1/traces: 6 steps
  conformance: FAIL
    step 2 is not a transition of the model from the state reached
      quint:    writeManifest(px(1, 1, 1, 1, ParquetDone), Ok)
      observed: writeManifest(batch=1, gen=g20260924T100000, payload=req-1) (seq 2, span efdc40821aced020)
```

Each scenario, each of the three paths (`run-demo.sh` output):

```
=== scenario happy
  otel      OTLP/JSON spans              exit 0  conformance: PASS;invariant commitImpliesData: PASS;invariant sealMatchesManifests: PASS;invariant committedNoDuplicatePayload: PASS
  steps     native step log              exit 0  conformance: PASS;invariant commitImpliesData: PASS;invariant sealMatchesManifests: PASS;invariant committedNoDuplicatePayload: PASS
  itf       model-level ITF              exit 0  conformance: PASS;invariant commitImpliesData: PASS;invariant sealMatchesManifests: PASS;invariant committedNoDuplicatePayload: PASS
=== scenario ambiguous-seal
  otel      OTLP/JSON spans              exit 1  conformance: PASS;...;invariant sealMatchesManifests: FAIL;violated after step 16;invariant committedNoDuplicatePayload: FAIL;violated after step 8
  steps     native step log              exit 1  conformance: PASS;...;invariant sealMatchesManifests: FAIL;violated after step 16;invariant committedNoDuplicatePayload: FAIL;violated after step 8
  itf       model-level ITF              exit 1  conformance: PASS;...;invariant sealMatchesManifests: FAIL;violated after step 16;invariant committedNoDuplicatePayload: FAIL;violated after step 8
=== scenario manifest-first
  otel      OTLP/JSON spans              exit 1  conformance: FAIL;step 2 disabled
  steps     native step log              exit 1  conformance: FAIL;step 2 disabled
  itf       model-level ITF              exit 1  conformance: FAIL;step 2 disabled
```

(`rotation-race` passes on all three; `no-lock` fails at step 2 on all three.)

## The publisher in a simulation (`sim/`, woven by `qenv/weave`)

The publisher uses plain Go concurrency: a `sync.RWMutex` that pushes hold
shared and rotation exclusively, a `sync.WaitGroup`, and a background seal
started with `go`. Built with Orchestrion and `qenv/weave` in
`orchestrion.tool.go`, those calls become qenv's:
- a lock is a scheduling point, and waiting on it is a simulated wait;
- the seal's goroutine (marked `//qenv:go`) is a simulation task.

So the publisher runs as written, its interleavings chosen by a seed or by
Hegel. Two changes were needed, both flagged by `quintgo lint -weave`:
- the `//qenv:go` comment on the go statement;
- `acquire` returns its unlock as a function literal calling `RUnlock`,
  not the method value `st.mu.RUnlock`, because weaving rewrites calls.

The environment:
- S3 writes fail or fail ambiguously (`fault.s3.*`) and take a chosen
  time, during which the clock moves and other pushes run;
- the collector retries a failed export, also in the next incarnation;
- the exporter crashes into new epochs.

```sh
go run -C ../../qweave ./cmd/qweave -C "$PWD" go test ./sim/ -v   # the repository's weaver; ~10 min, -short ~4 min
go run github.com/DataDog/orchestrion go test ./sim/ -v            # Orchestrion weaves it too
```

| Test | Shows |
|---|---|
| `TestExploredRunsConform` | 40 seeds: every interleaving of pushes, rotations and background seals, with failures, ambiguous writes and crashes, is a behaviour of the model |
| `TestInvariantsHoldWithoutAmbiguousWrites` | with plain failures only, the design's invariants hold too |
| `TestExplorationFindsAmbiguousFindings` | with ambiguous writes, random runs find the model's known findings F1/F3 (9 of 60 seeds), which `ambiguous-seal` had reproduced by hand |
| `TestMutants` | `ManifestBeforeTable` (every seed), `NoRotationLock` (34 of 40), `BackwardRotation` (7 of 150: a narrow window) |
| `TestHegelShrinksNoRotationLock` | Hegel shrinks to a rotation while a push is between its writes |
| `TestLoadRecovers` | 3 requests/s against an S3 that serves 10 writes/s, through a 3x slowdown: recovers with 4 exporters or 16 |
| `TestTimeoutsMakeDuplicates` | with 16 exporters and a 2 s S3 timeout, S3's queueing delay turns writes ambiguous and the collector's retries commit requests twice, with no trigger at all (30 in a run); with a 10 s timeout, none |
| `TestLoadRunAgainstModel` | a load run's 353 steps conform to the model, and its duplicate is the model's F3 |

What the simulation found:
- **The stand-in was out of date.** Exploring the faithful publisher, one
  run in 40 rotated backwards: a `Tick` read the clock, waited for the
  exclusive lock while pushes held it shared through slow S3 writes, and
  meanwhile a push rotated past the minute. With the lock, `Tick` saw a
  different id and rotated back, reopening a sealed generation. The real
  `publish.go` had already been fixed ("Generations only move forward. The
  clock is read under the lock…"), and the stand-in had been copied
  before the fix. It now follows `publish.go`; the old code is the
  `BackwardRotation` mutant.
- **Load does not collapse the exporter. It produces duplicates.** Its
  concurrency is bounded and the collector pauses before retrying, so
  there is no storm. But an S3 write timeout shorter than S3's queueing
  delay turns load into ambiguous writes, and retried ambiguous writes
  into duplicate commits. The model's F3 then happens in normal operation.
  Their number grows with concurrency, and a timeout above the worst
  queueing delay removes them.
- **Two qenv bugs, fixed in qenv.**
  - The background seal ran with `context.Background()`, so its simulated
    S3 latency slept in real time (5–30 s per run). qenv operations now
    find the task through the goroutine when the context has none; a run
    takes 3 ms.
  - `//quint:action` records when its function returns, and a task killed
    by a crash returns by unwinding. That recorded a write the crash had
    interrupted, as successful. Steps recorded while a killed task
    unwinds are now discarded.

## Known gaps

- With `--backend rust`, `quint run --mbt` traces of `currentDesign`
  include schedules the driver cannot reproduce yet. One example is a
  `pushStart` right after a crash and an idle rotation: the stand-in waits
  for a write that the gate never sees, and the step times out ("the
  implementation made no write within 2s"). The spike verified the MBT
  tests on the typescript backend, so `mbt` defaults to
  `QUINT_BACKEND=typescript` and seed 42. Set `QUINT_BACKEND=rust` to
  reproduce the gap.
- The stand-in, not the real exporter, is validated here. The real one
  is checked from trace files in casselc/observability (`otel-chdb/PBT.md`).
  The simulation could run the real one too: it needs the same
  `//qenv:go` mark on its background seal, and its unlock method value
  wrapped. Its chDB sessions would also need a seam, as the stand-in's
  `Store` is.
- Hegel cannot find `BackwardRotation` within its budget here: the window
  is a few runs in a hundred, and every case is a quint check. Seeded
  exploration finds it.
