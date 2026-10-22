# Glomers broadcast on gosim, choices from qenv

The Gossip Glomers broadcast node ([../glomers/broadcast](../glomers/broadcast)),
unchanged, on three machines simulated by
[gosim](https://github.com/jellevandenhooff/gosim). Nodes talk over real TCP
([tcp](tcp)): `net.Listen`, `net.Dial`, `bufio`, `encoding/json`, tickers,
goroutines and mutexes, all run deterministically by gosim. Every choice
gosim makes (the schedule, `select` cases, map iteration order, and what the
test asks for: the client's picks and the partitions) comes from a qenv
`Chooser` in the test process. The steps the nodes record with qobs are
checked against [the broadcast model](../glomers/model/broadcast.qnt) with
the Glomers binding, like the runs of the Maelstrom simulator.

This is the spike of DESIGN.md §14. gosim needed a fork, kept here as a
patch: [gosim.patch](gosim.patch), applied by [fetch-gosim.sh](fetch-gosim.sh)
to the upstream commit it was made on.

```sh
./fetch-gosim.sh        # clones gosim into .gosim-fork and applies the patch
go test -short ./...    # FULL: go test ./... (adds the Hegel test, ~5 min)
./run-demo.sh
```

## How it fits together

```
go test (host_test.go)                    gosim test binary (cluster/, translated)
  qenv.Chooser  ◄── Choose(kind, n) ────  scheduler, select, maps, gosimruntime.Choose
  (Seeded, Hegel, Replay, Recorder)
  qtrace steps  ◄── Emit(step line) ────  qobs.JSONLSink in each node
  qtest / quinthegel judge the run
```

gosim translates the code under test, and everything it imports, into a
module of its own that runs on gosim's runtime. Hegel cannot run in there
(it calls a native library), so the test is split: `cluster/` is the
simulated side (`//go:build sim`) and `host_test.go` drives it over gosim's
metatest protocol, which the fork extends with choices and emitted lines
(`metatesting.MetaT.RunWith`).

## Tests

| test | chooser | mutation | expected verdict |
|---|---|---|---|
| `TestSeededRunsConform` | `qenv.NewSeeded`, partitions at 30% | none | conforms, both invariants hold |
| `TestReplayIsDeterministic` | `qenv.Recorder`, then `qenv.Replay` | none | identical steps from the same ~5500 choices |
| `TestReadOwnOnlyRejected` | `qenv.NewSeeded` | `ReadOwnOnly` | `qtest.Mismatch("read")` |
| `TestHegelFindsNoRetry` (not `-short`) | `qenv.Hegel` | `NoRetry` | `quinthegel.FindViolation`: `convergedWhenSettled`, shrunk to gossip lost to an isolated node |

## What the spike found

- gosim's randomness has one source, a seeded generator in its scheduler,
  so a `Chooser` can take it over with a small patch: bounded choices by
  kind, unbounded randomness (math/rand, crypto/rand) from a generator
  seeded by one choice. Option 0 is nominal (the first runnable goroutine,
  the first ready case, no fault), as qenv's convention needs for Hegel's
  shrinking. Replaying the recorded choices reproduces a run exactly.
- Go 1.26 support took about 40 hooks and a handful of translator fixes
  (see `gosim.patch`): `sync.Mutex` moved to `internal/sync`, `sync.Map`
  became a hash trie, weak pointers, synctest, the FIPS 140 crypto packages
  (built `purego`), new reflect API, `O_DIRECTORY`. gosim's own behaviour
  suite passes on 1.26.1; its race mode does not build yet.
- A choice round trip to the test process costs about 50 µs: a run of this
  cluster takes 350 ms with qenv choosing, 60 ms with gosim's own seed.
  Most choices are the scheduler's. Handing the simulation entropy in
  chunks, as `qenv.Hegel` already draws it, would remove most of that.
