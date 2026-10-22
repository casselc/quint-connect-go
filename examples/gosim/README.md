# Examples on gosim, every choice from qenv

Two systems run in [gosim](https://github.com/jellevandenhooff/gosim), on a
patched gosim ([../../qgosim](../../qgosim)). Every choice gosim makes (the
schedule, `select` cases, map iteration order, and what the code asks for:
faults, a client's picks) comes from a qenv `Chooser` in the test process.
The steps the code records with qobs come back to that test and are
checked against the system's Quint model with its usual binding.

- [broadcast](broadcast): the Gossip Glomers broadcast node
  ([../glomers/broadcast](../glomers/broadcast)), unchanged, on three gosim
  machines over real TCP ([broadcast/tcp](broadcast/tcp)): `net.Listen`,
  `net.Dial`, `bufio`, `encoding/json`, tickers, goroutines and mutexes,
  all simulated. A nemesis isolates nodes.
- [edgepublish](edgepublish): the edgepublish publisher
  ([../edgepublish](../edgepublish)) in its simulated environment
  (`sim.RunOn`). It is not woven by Orchestrion, and has no qenv/weave:
  its `sync.RWMutex`, `sync.WaitGroup` and background goroutine are
  gosim's. Its `//quint:action` annotations are woven by gosim's
  translator, from the qobs aspects that [gosim.tool.go](gosim.tool.go)
  names.
- [checkout](checkout): the checkout saga ([../checkout](../checkout)),
  its deployment unchanged (`checkout.Deploy`), on qenv's native runtime
  (DESIGN.md §15.1). Its tasks are goroutines, its processes are gosim
  machines that crash with all their goroutines, and its time is gosim's.
  The model, binding and expected verdicts are those of the qenv-scheduled
  tests.

```sh
../../qgosim/fetch-gosim.sh   # clones the fork (branch gosim-fork) into qgosim/.gosim-fork
go test -short ./...          # FULL: go test ./... (adds the Hegel test, ~5 min)
./run-demo.sh
```

## How it fits together

```
go test (host_test.go)                    gosim test binary (translated, //go:build sim)
  qenv.Chooser  ◄── Choose(kind, n) ────  scheduler, select, maps, qgosim.Chooser
  (Seeded, Hegel, Replay, Recorder)
  qtrace steps  ◄── Emit(step line) ────  qobs into qgosim.Sink
  qtest / quinthegel judge the run
```

gosim translates the code under test, and everything it imports, into a
module of its own that runs on gosim's runtime. Hegel cannot run in there
(it calls a native library), so each test is split:
- the simulated side is an ordinary gosim test, built with the sim tag;
- `host_test.go` drives it with `qgosim.Run`, or with `qgosim.Scenario`
  as a `qtest.System`'s Run, whose Explore runs seeds in parallel over a
  pool of test binaries.

## Tests

| test | chooser | mutation | expected verdict |
|---|---|---|---|
| `broadcast.TestSeededRunsConform` | `qenv.NewSeeded`, partitions at 30% | none | conforms, both invariants hold |
| `broadcast.TestReplayIsDeterministic` | `qenv.ChoiceLog`, then `qenv.Replay` | none | identical steps from the same ~5500 choices |
| `broadcast.TestReadOwnOnlyRejected` | `qenv.NewSeeded` | `ReadOwnOnly` | `qtest.Mismatch("read")` |
| `broadcast.TestHegelFindsNoRetry` (not `-short`) | `quinthegel.Choices` | `NoRetry` | `quinthegel.FindViolation`: `convergedWhenSettled`, shrunk to gossip lost to an isolated node |
| `edgepublish.TestExploredRunsConformOnGosim` | `qtest.Explore`, crashes of the exporter's machine | none | conforms |
| `edgepublish.TestInvariantsHoldWithoutAmbiguousWritesOnGosim` | `qtest.Explore` | none, no ambiguous writes | conforms, invariants hold |
| `edgepublish.TestMutantsOnGosim` | `qtest.Explore`, no crashes | `ManifestBeforeTable` | `qtest.Rejects("writeManifest")` |
| | | `NoRotationLock` | a rotation or write the model rejects (29 of 30 runs) |
| `checkout.TestFaithfulConformsOnGosim` | `qtest.Explore`, crashes 1 in 20 | none, at each isolation level | conforms |
| `checkout.TestMutantsOnGosim` | `qtest.Explore` | `LockWithoutWrite` at snapshot isolation | a ship or cancel the model rejects (write skew) |
| | | `NoIdempotencyKey` | `chargedAtMostOnce` or `settledCorrectly` violated |

## What it showed

- gosim's randomness has one source, a seeded generator in its scheduler,
  so a `Chooser` can take it over with a small patch:
  - bounded choices go by kind;
  - unbounded randomness (math/rand, crypto/rand) comes from a generator
    seeded by one choice;
  - option 0 is nominal (the first runnable goroutine, the first ready
    case, no fault), as qenv's convention needs for Hegel's shrinking.

  Replaying the recorded choices reproduces a run exactly.
- qenv's doubles and processes run on gosim unchanged, behind the native
  runtime. A run of checkout makes about 27,000 choices, 89% of them the
  scheduler's.
- The NoIdempotencyKey mutant shows a second symptom on gosim. A
  cancellation's void, which looks the charge up by the order's key,
  finds nothing under the mutant's per-attempt key, so the order stays
  paid. qenv's scheduler finds the double charge first.
- The host hands the simulation entropy in 512-byte chunks, for every
  choice but faults (`qenv.EntropyOf`), as `quinthegel.Choices` draws it.
  A broadcast run takes 48 ms that way. One round trip per choice took
  438 ms, and gosim's own seed takes 60 ms. A Recorder records the
  chunks, so `qenv.Replay` still repeats a run exactly.
