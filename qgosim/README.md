# qgosim

Runs qenv deployments, and whole programs, in
[gosim](https://github.com/jellevandenhooff/gosim), with every choice made
by a qenv `Chooser` in the test process (DESIGN.md §14 and §15.1). It is a
module of its own because it depends on gosim. It holds the fork, as a
patch on the upstream commit it was made on:

```sh
./fetch-gosim.sh    # clones gosim into .gosim-fork and applies gosim.patch
```

A module using it replaces `github.com/jellevandenhooff/gosim` with that
directory (see [../examples/gosim/go.mod](../examples/gosim/go.mod)).

- In the simulation (`//go:build sim`):
  - `New` returns a native `*qenv.Sim` whose choices are the host's,
    whose processes are gosim machines and whose context records steps
    to the host;
  - or use `Chooser`, `Platform` and `Sink` directly, for programs
    without qenv.
- On the host:
  - `Run(t, pkg, test, chooser, env...)` runs a simulated test and
    returns its steps;
  - `Scenario` is the same, for `qtest.Explore`.

The patch:
- ports gosim to Go 1.26;
- routes every bounded choice through a Chooser (`gosimruntime.Choose`);
- adds `gosimruntime.Emit`;
- extends the metatest protocol: `metatesting.MetaT.RunWith`, `Start`,
  `Close` and `Alive`.

gosim's race mode does not build on 1.26 yet.
