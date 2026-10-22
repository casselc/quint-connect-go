# A whole program, network and channels included: gosim

When channels, `select`, the standard library's own goroutines, TCP or the
disk matter, run the code in gosim. Use package qgosim, its own module, on
a patched gosim fetched by `qgosim/fetch-gosim.sh`; examples/gosim shows
both kinds of code:
- **a qenv deployment, unchanged**: in a simulated test, `checkout.RunOn(qgosim.New(), cfg)`.
  `qgosim.New` is qenv's native runtime (`qenv.NewNative`):
  - tasks are goroutines;
  - processes are gosim machines, and a crash stops all of a machine's
    goroutines;
  - time is gosim's;
  - choices are the host's.

  Give a `Run`-style function a `*qenv.Sim` parameter so that both
  runtimes can call it.
- **any program**: start machines with `gosim.NewMachine`, and ask for
  faults with `gosimruntime.Choose("fault.<name>", n)`, where option 0
  is no fault. Record with qobs into `qgosim.Sink()`.
- **code annotated with `//quint:action`**: add a `gosim.tool.go`
  (`//go:build tools`) to the module that imports
  `github.com/casselc/quint-connect-go/qobs`. The translator then weaves
  qobs's aspects, as `orchestrion go test` would. Leave qenv/weave out:
  gosim simulates locks and goroutines itself, so code that needed weaving
  on qenv's scheduler runs as it is (examples/gosim/edgepublish).

On either kind:
- put the simulated test under `//go:build sim`, and its host test under
  `//go:build !sim` when they share a package;
- keep Hegel and anything native out of the simulated package's imports
  (gosim translates them all). qenv, qobs, qload and qmeta have none:
  Hegel adapters live in quinthegel (`quinthegel.Chooser`,
  `quinthegel.FindTrigger`). Do the same with your own: Hegel code in a
  package the simulation does not import;
- drive it from the host with `qgosim.Run(t, pkg, test, chooser,
  env...)`, which returns the steps. The chooser can be
  `qenv.NewSeeded`, `quinthegel.Chooser(tc)`, a `qenv.Recorder` or `qenv.Replay`.
  Or use `qtest.Explore(t, binding, x, qgosim.Scenario(t, pkg, test))`,
  which runs seeds in parallel, one test binary each;
- check the steps with `qtest.Check` or `qtest.Validate`, or add them
  with `rec.Add(steps...)` inside `quinthegel.FindViolation`.

Each choice is a round trip to the test process, about 50 µs, so a gosim
run is far slower than one on qenv's scheduler. Explore and shrink there,
and use gosim to confirm with the real standard library and network.
