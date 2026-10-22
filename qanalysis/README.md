# qanalysis

quint-connect-go's checks as `golang.org/x/tools/go/analysis` passes, and
the static action map. Its own module: the core depends only on OTel,
yaml.v3 and Hegel.

| Analyzer | Reports | Same as |
|---|---|---|
| `quintsim` | in files importing qenv: `go` statements, the wall clock, steps recorded with `context.Background()` | `quintgo lint -sim` |
| `quintweave` | unmarked `go` statements, sync method values, embedded sync types (for packages woven by qenv/weave) | `quintgo lint -weave` |
| `quintaction` | `//quint:action` without `action:`; expressions using locals (only parameters, the receiver and named results are in scope on return); with `-binding`: actions not in the model, fields the binding uses but the annotation does not record | `quintgo lint -binding -src` |

The first two wrap package `qlint`, so the CLI and the analyzers find the
same things.

```sh
go install github.com/casselc/quint-connect-go/qanalysis/cmd/quintvet@latest
quintvet ./...                                           # all three
quintvet -quintweave=false -quintaction.binding app.binding.yaml ./...
go vet -vettool=$(which quintvet) -quintweave=false ./... # per package, with go vet
```

**golangci-lint**: a module plugin named `quintgo` (package `golangci`;
settings `analyzers`, `binding`, `quint`). See the package doc for
`.custom-gcl.yml` and `.golangci.yml`.

**gopls** loads only the analyzers compiled into it: use the binary, go vet
or golangci-lint (which editors run).

## Static action map (`actionmap`, `cmd/actionmap`)

For each model action, the code statically reachable from where its steps
are recorded (a `//quint:action` function, or one calling `qobs.Record`
with a constant action, closures included), through a VTA call graph that
stops at other packages' functions. With qcover's per-step coverage
(`-coverage summary.json`) it reports per action:

- **reachable, never run**: a path no run took;
- **reachable, run only under other actions**: shared code, or a path of
  this action the runs never took;
- **run by its steps, not reachable from a site**: an attribution gap,
  such as code in the site's callers (the loop that leads to the step) or
  setup that ran before a task's first step.

```sh
QUINTGO_KEEP=/tmp/k go run -C qweave ./cmd/qweave -C .. -cover \
  -coverpkg github.com/casselc/quint-connect-go/examples/checkout \
  go test -run '^TestCodeCoverage$' ./examples/checkout/
cd qanalysis && go run ./cmd/actionmap -C .. -coverage /tmp/k/checkout-coverage.json ./examples/checkout/...
```

Tests: `go test ./...` (analysistest fixtures in `testdata/src`, with
stubs of qenv and qobs; the binding test and the checkout comparison need
quint, and the latter skips under `-short`).
