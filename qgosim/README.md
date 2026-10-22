# qgosim

Runs qenv deployments, and whole programs, in
[gosim](https://github.com/jellevandenhooff/gosim), with every choice made
by a qenv `Chooser` in the test process (DESIGN.md §14 and §15.1). It is a
module of its own because it depends on gosim. The fork is the
`gosim-fork` branch of this repository (upstream's history plus our
commits; its FORK.md says what they change), until it gets a repository of
its own:

```sh
./fetch-gosim.sh    # clones the branch into .gosim-fork, at the commit qgosim pins
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

The fork:
- ports gosim to Go 1.26;
- routes every bounded choice through a Chooser (`gosimruntime.Choose`);
- adds `gosimruntime.Emit`;
- extends the metatest protocol: `metatesting.MetaT.RunWith`, `Start`,
  `Close` and `Alive`;
- with a chooser that has entropy (`qenv.EntropyOf`), makes every choice
  but faults from bytes the host hands out in chunks of 512, one round
  trip each;
- weaves Orchestrion aspects before translating. The module's
  `gosim.tool.go` (else `orchestrion.tool.go`) names the packages whose
  `orchestrion.yml` apply, so `//quint:action` and `//quint:thread`
  (qobs's aspects) record steps in a simulation without an Orchestrion
  build. Supported so far: function-body join points with a directive,
  and prepend-statements advice. Don't list qenv/weave: gosim simulates
  `sync` and goroutines itself.

`New` installs the run's recorder as qobs's global recorder on every
machine (gosim gives each its own globals). Code that records with
`context.Background()`, such as a goroutine started with `go`, then
records into the run.

gosim's race mode does not build on 1.26 yet.
