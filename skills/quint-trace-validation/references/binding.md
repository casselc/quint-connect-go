# Binding rules, lint and analyzers

Rules:
- A parameter with no template is the step field of its name, rendered as
  a Quint literal. Set, list and map parameters (type aliases included)
  are converted from the recorded JSON by `toSet`, `toList` and `toMap`.
  Give a template only to map a field differently, e.g.
  `args: { o: '{{.outcome}}' }`. An action that needs nothing else can
  be `name: {}`.
- A failed step whose templates ignore `{{.outcome}}` must be mapped in
  the action's `outcomes` (`skip`, or `call` to replay it as a success).
  The tool refuses to guess.
- To also emit a model-level ITF in-process, write argument templates as
  Quint literals (no operator calls). `expect` may be any Quint boolean.

Then lint: `quintgo lint -binding bank.binding.yaml -src ./pkg`. It checks
the binding against `quint typecheck`, the annotations against the
binding, and the literal action of every `qobs.Record` call against the
model (a misspelled action gets a did-you-mean).

In the editor and in CI, the same checks run as analyzers (qanalysis):
- `quintvet ./...` (built from `qanalysis/cmd/quintvet`, or
  `go vet -vettool=$(which quintvet)`) runs the simulation, weaving and
  annotation checks; `-quintaction.binding app.binding.yaml` adds the
  binding's. In golangci-lint, enable the `quintgo` module plugin.
- An annotation's expressions run on return: use parameters, the
  receiver and named results. quintaction names a local it sees.
- `actionmap -coverage summary.json ./...` (the summary of a woven
  coverage run) lists per action what its code can reach but no run ran,
  and what its steps ran that it cannot reach (callers' and setup code).

