# examples/kvstore: an HTTP service checked against its model

A key-value service with versioned compare-and-swap:
- `kv/`: the store, the HTTP server and a client;
- `cmd/kvserver`: the service;
- `cmd/kvload`: a concurrent load generator.

`model/kv.qnt` is the service's Quint model.

The service is instrumented the way a production service would be. It
uses no Orchestrion and no build changes:
- the store reports each operation as a model step (`qobs.Record`) from
  inside the critical section that orders it;
- the HTTP server emits one ordinary OTel span per request, with the
  attributes an operator wants anyway.

Run it, put load on it, then check what it recorded:

```sh
./run-demo.sh                          # faithful: both checks pass
MUTATION=stale-cas ./run-demo.sh       # a race: two CAS with the same expected version both win
MUTATION=revision-on-conflict ./run-demo.sh
```

```
$ quintgo validate -binding kv.binding.yaml -convert kv.convert.yaml spans.jsonl      (MUTATION=stale-cas)
  conformance: FAIL
    step 112 is not a transition of the model from the state reached
      quint:    cas("a", 39, "y", Ok)
      observed: cas(expected=39, key=a, value=y) (seq 112, span 85ea63b66b1bd28e)
      obs:      revision=46
```

Or by hand:

```sh
go run ./cmd/kvserver -addr 127.0.0.1:8080 -steps steps.jsonl -spans spans.jsonl &
go run ./cmd/kvload -addr 127.0.0.1:8080 -clients 8 -ops 100
kill -INT %1
quintgo validate -binding kv.binding.yaml steps.jsonl                              # the model steps
quintgo validate -binding kv.binding.yaml -convert kv.convert.yaml spans.jsonl     # the plain spans
```

## What it shows

- **Checking a concurrent service.** The model is sequential. A
  concurrent run conforms if some serial order explains it: every CAS
  outcome, revision and read must be consistent. The recording gives that
  order:
  - steps are recorded under the store's lock;
  - for spans, the server stamps `kv.op`, the store's linearization
    index, and `kv.convert.yaml` uses it as an ordering key
    (`order: { op: attr:kv.op }`). Without it, spans of concurrent
    requests would be ordered by end time, which is not the order the
    store applied them in.
- **Errors that classify themselves.** `ErrConflict` and `ErrNotFound`
  implement `QuintOutcome()`. The binding maps `conflict` and `missing` to
  the model's `Outcome` values, so a CAS's outcome is checked as well as
  its effect.
- **Reads checked against the model.** A read is a model step that
  changes nothing. Its `expect` checks what it returned
  (`obs.found`, `obs.value`, `obs.version`) against the model's store.
- **Long runs.** A 400-step run is replayed in segments, because Quint's
  Rust evaluator limits `.then` nesting to about 50.

## Tests (`kvstore_test.go`)

| Test | Direction | Shows |
|---|---|---|
| `TestServiceUnderLoadConforms` | 8 concurrent clients; recorded steps checked (`qtest`) | a concurrent run is a serial behaviour of the model |
| `TestServiceSpansConform` | the same load; plain spans converted and checked | spans plus `kv.op` give the same verdict |
| `TestStaleCASCaughtUnderLoad` | the race mutant under load | caught as a CAS the model cannot take |
| `TestModelDrivesService` | model traces drive the HTTP API (`connect.Run`) | store and revision match the model after every request |
| `TestHegelDrivesService` | a Hegel state machine over HTTP; runs checked after the test | |
| `TestHegelShrinksRevisionOnConflict` | the same machine against a mutant, checked per case | Hegel shrinks to one failed CAS |

```sh
go test ./examples/kvstore/ -v
```
