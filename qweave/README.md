# qweave

Weaves Orchestrion aspects into Go source with type information, and
builds the result with the go command's `-overlay`. It reads the aspect
files (`orchestrion.yml`) of the packages a module's tool file imports
(`orchestrion.tool.go`, or `gosim.tool.go` for gosim), so the same
annotations work under qweave, Orchestrion and gosim:

```sh
go install github.com/casselc/quint-connect-go/qweave/cmd/qweave@latest   # or go run from this module
qweave go test ./...                 # weave the module's package graph, then go test -overlay
qweave -v go build ./cmd/server      # list what was woven and what was skipped
```

The woven files may import packages the originals do not. The go command
computes the build from the overlay, so those imports are built and linked
like any other. `//line` directives keep compiler errors and stack traces
on the original lines. qgosim feeds the same overlay to gosim's translator
(`GOSIM_OVERLAY`).

**What it supports**
- Join points:
  - `function-body` with a directive;
  - `method-call` (receiver, name, value-only or pointer-only);
  - `function-call`;
  - `function` (a function literal);
  - `directive` on the enclosing statement;
  - `package-filter: {root: true}`;
  - the combinators `all-of`, `one-of` and `not`.
- Advice: `prepend-statements`, `wrap-expression` and `inject-declarations` (added at the end of the join point's file), several per aspect.
- Template data, as Orchestrion names it: `.Function.Name`, `.Function.ArgumentOfType` and
  `.ResultOfType` (unnamed results are named for the advice),
  `.DirectiveArgs`, `.AST.Fun.X`, `.AST.Fun.Sel` and `.AST.Args`, and the
  expression itself (`{{ . }}`).

Aspects that use anything else are skipped and listed with `-v`.
Matches nest: a woven call inside a woven function literal is rendered
from the inner advice's output.

**Beyond Orchestrion.** qweave matches on types, not syntax, so it also
weaves:
- **method values.** `return s.mu.RUnlock` becomes a function literal
  that binds the receiver when the value is made, as a method value does,
  and makes the advised call;
- **promoted calls.** `c.Lock()` on `struct{ sync.Mutex }` matches the
  embedded field's type, and the advice is applied to `(c).Mutex`.

`quintgo lint -weave` reports both, but they are a problem only under
Orchestrion.

Tests: `go test ./...` weaves two fixtures (`testdata/`). It checks the
woven text, and runs the sync fixture in a qenv simulation, where its
embedded lock, method-value unlock and goroutine would hang the scheduler
unwoven.
