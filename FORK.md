# gosim, forked for quint-connect-go

This branch (`gosim-fork` of casselc/quint-connect-go) is
[gosim](https://github.com/jellevandenhooff/gosim) at ffd3a61 with the
changes quint-connect-go needs, kept as ordinary commits on top of
upstream's history so that they can be rebased onto a newer upstream:

- Go 1.26 support: hooks for the runtime entry points added or moved since
  Go 1.23 (`internal/hooks/go123/go126.go`), the translator reporting every
  missing hook in one run, `purego` builds for the FIPS 140 crypto
  packages, reflect's newer API, `O_DIRECTORY`.
- A `Chooser` (`gosimruntime/choose.go`): every bounded choice the runtime
  makes (schedule, select, map order) and `gosimruntime.Choose` go through
  it; unbounded randomness comes from a generator seeded by one choice.
  The metatest protocol lets the runner be the chooser and receive the
  lines the program `Emit`s (`metatesting.MetaT.RunWith`), and runs
  binaries in parallel (`Start`, `Close`, `Alive`).

quint-connect-go uses it through `qgosim` (its `fetch-gosim.sh` clones
this branch). It moves to a repository of its own later. Race mode
(`-race`) does not build on Go 1.26 yet.
