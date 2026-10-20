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
/tmp/q/quintgo lint -binding binding.yaml -src .
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
| `ambiguous-seal` | a manifest PUT lands but reports failure; the retry commits again; the seal counts 1 of 2 | conforms, but `sealMatchesManifests` fails after step [15] and `committedNoDuplicatePayload` after step [7]: the model's known finding F1/F3, now seen in running code |
| `manifest-first` | MUTANT: the manifest is written before the table insert | **FAIL** at step [1]: `writeManifest(px(1, 1, 1, 1, ParquetDone), Ok)` is not a transition (QNT513) |
| `no-lock` | MUTANT: the push drops the shared lock before its writes | **FAIL** at step [1]: `rotateGen` is not enabled while a push is in flight; `quintgo steps` shows batch 1 committed into generation 1 after its `sealGen` |

```
=== scenario manifest-first
actor edge-1/traces: 6 steps
  conformance: FAIL
    step [1] is not a transition of the model from the state reached
      quint:    writeManifest(px(1, 1, 1, 1, ParquetDone), Ok)
      observed: seq 2 writeManifest(batch=1, gen=g20260924T100000, payload=req-1) (span efdc40821aced020)
      Error [QNT513]: Cannot continue in `then` because the highlighted expression evaluated to false
```

Each scenario, each of the three paths (`run-demo.sh` output):

```
=== scenario happy
  otel      OTLP/JSON spans              exit 0  conformance: PASS;invariant commitImpliesData: PASS;invariant sealMatchesManifests: PASS;invariant committedNoDuplicatePayload: PASS
  steps     native step log              exit 0  conformance: PASS;invariant commitImpliesData: PASS;invariant sealMatchesManifests: PASS;invariant committedNoDuplicatePayload: PASS
  itf       model-level ITF              exit 0  conformance: PASS;invariant commitImpliesData: PASS;invariant sealMatchesManifests: PASS;invariant committedNoDuplicatePayload: PASS
=== scenario ambiguous-seal
  otel      OTLP/JSON spans              exit 1  conformance: PASS;...;invariant sealMatchesManifests: FAIL;violated after step [15];invariant committedNoDuplicatePayload: FAIL;violated after step [7]
  steps     native step log              exit 1  conformance: PASS;...;invariant sealMatchesManifests: FAIL;violated after step [15];invariant committedNoDuplicatePayload: FAIL;violated after step [7]
  itf       model-level ITF              exit 1  conformance: PASS;...;invariant sealMatchesManifests: FAIL;violated after step [15];invariant committedNoDuplicatePayload: FAIL;violated after step [7]
=== scenario manifest-first
  otel      OTLP/JSON spans              exit 1  conformance: FAIL;step [1] disabled
  steps     native step log              exit 1  conformance: FAIL;step [1] disabled
  itf       model-level ITF              exit 1  conformance: FAIL;step [1] disabled
```

(`rotation-race` passes on all three; `no-lock` fails at step [1] on all three.)

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
