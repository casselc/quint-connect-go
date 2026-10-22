# Notes for merging C1 and C5 (qanalysis)

Fold these into the root docs, then delete this file.

## Core changes (all small)

- `internal/lint` → `qlint` (exported, std-only): `SimFile(fset, f)` and
  `WovenFile(fset, f)` are the per-file checks; `Finding.At` is the token
  position. `Sim`/`Woven` are unchanged, and so are their output and tests.
  `cmd/quintgo` imports `qlint`.
- `binding.AnnotationsOf(fset, f)`: per-file annotation scan, which
  `ScanAnnotations` now uses. `Annotation` gains `At token.Pos` and
  `Decl *ast.FuncDecl`.
- `examples/checkout/coverage_test.go`: with `QUINTGO_KEEP` set, writes
  `checkout-coverage.json` (the `qcover.Summary`) for actionmap.

## README rows

- Package table:
  `| qanalysis | separate module: go/analysis passes quintsim, quintweave, quintaction (cmd/quintvet, go vet -vettool, a golangci-lint module plugin); actionmap: each action's statically reachable code compared with its steps' coverage (cmd/actionmap) |`
  `| qlint | the simulation and weaving lints (quintgo lint -sim, -weave), per file for the analyzers |`
- Entry points: `quintvet ./...` (or `go vet -vettool=$(which quintvet)`);
  `actionmap -coverage summary.json ./...`.

## AGENTS.md

Layout, replacing the `internal/lint` mention and adding:

```
qlint/        simulation and weaving lints (quintgo lint -sim, -weave); per file for qanalysis
qanalysis/    separate module (x/tools): analyzers (cmd/quintvet, golangci plugin), static action map (cmd/actionmap)
```

Commands: `cd qanalysis && go test ./...` (the checkout comparison weaves
and explores checkout: skipped with -short, needs quint).

Gotchas:
- quintaction's scope check uses `LookupParent(name, body.Lbrace)`:
  go/types declares a function body's top-level locals in the same scope
  as its parameters, so a lookup without the position finds locals.
- analysistest fixtures are GOPATH-style with stub qenv and qobs
  packages (`qanalysis/testdata/src/github.com/...`): keep the stubs to
  what the fixtures use. An expectation on a `//quint:action` line goes
  in a block comment before the directive (the directive runs to the end
  of the line).
- actionmap stops at functions outside the loaded packages: a library
  that calls back (qenv's commit hooks) is one call-graph node for every
  closure handed to it, and going through it made cancel "reach"
  PlaceOrder. A closure is named by its enclosing declaration, as qcover
  names its blocks.

## DESIGN section draft: §30 Analyzers and the static action map (C1, C5)

**Analyzers (C1).** `quintgo lint -sim` and `-weave` were directory
walks; the analyzers wrap the same per-file checks (`qlint.SimFile`,
`WovenFile`), so the CLI and quintvet agree finding for finding, and
`quintgo lint` stays std-only. x/tools went into a new module,
qanalysis, not qweave: qweave is a build tool with no dependency on the
core, the analyzers need the core's binding and lint packages, and a
golangci-lint plugin is a dependency of its own that a weaver should not
carry. quintaction adds what the CLI could not check without types: an
annotation's expressions may use only parameters, the receiver and named
results, since the advice records on return, at the top of the body,
where locals are out of scope. With `-binding` it runs
`binding.LintAnnotations` per annotation (loading the model once per
process: it runs quint). Run on edgepublish with its binding it reports
the same as the CLI: `probe`'s `woven` action is not in the model (the
weaving probe; left as is).

gopls cannot load third-party analyzers; editors get them through
golangci-lint (the module plugin `quintgo`) or `go vet -vettool`.

**Static action map (C5).** A1 says which code each step ran; the map
says which code each action can run. Sites are `//quint:action`
functions and functions calling `qobs.Record` with a constant action
(checkout records in `OnCommit` closures, so a site's closures count as
the site). Reachability is a VTA call graph from the sites, stopping at
functions outside the loaded packages. Without the stop, every closure
handed to qenv's commit hooks was reachable from every other, and cancel
reached PlaceOrder. Names follow qcover (`pkg.(T).M`, closures under their
declaration), so the two maps compare function by function.

On checkout (30 crashy seeds at SI):
- `Deploy` is run by the first step of every task: setup, attributed to
  the task's next step (A1's rule), and reachable from no site;
- markPaid's steps run `RunPayments` and `inbox.Receive`, which `pay`
  does not reach: code that leads to a step lives in the site's callers,
  which downward reachability cannot see;
- `settle`'s site is the run driver (`RunOn`), so it reaches everything,
  and all of it runs under other actions: a driver-recorded step says
  nothing about code;
- nothing reachable from a shop site went unrun.

Deviation: "blocks" reachable was planned; the map is by function. qcover
blocks carry their function, and an SSA block is not a qcover block (the
weaver splits at statements, SSA at control flow), so blocks would need a
position join for little gain over A1's never-run list.

## Skill text (quint-trace-validation, or a lint section of the workflow skill)

- `quintvet ./...` runs the simulation, weaving and annotation checks as
  analyzers; `-quintaction.binding app.binding.yaml` adds the binding's
  checks. In golangci-lint, enable the `quintgo` module plugin.
- An annotation's expressions run on return: use parameters, the
  receiver and named results. quintaction names a local it sees.
- `actionmap -coverage summary.json ./...` (with a woven coverage run's
  summary) lists per action what its sites reach but no run ran, and what
  its steps ran that no site reaches (callers' and setup code).

## CI

```yaml
- name: qanalysis
  working-directory: qanalysis
  run: |
    go vet ./...
    go test ./...
- name: quintvet on the core
  run: |
    go build -C qanalysis -o "$RUNNER_TEMP/quintvet" ./cmd/quintvet
    "$RUNNER_TEMP/quintvet" -quintweave=false ./...   # clean on the core today
```

(`go test ./...` there includes the checkout comparison, about a minute
with weaving; use `-short` to skip it.)
