---
name: quint-model-for-go
description: Write or adapt a Quint specification so Go code can be tested against it with quint-connect-go (model-based tests, Hegel state machines, trace validation). Use when creating a .qnt model of a Go component, when quint run/test/typecheck fails, or when a connect/quinthegel/validate error points at the model (anonymous action, missing nondet pick, constants, restart action).
---

# Quint models that Go code can be checked against

quint-connect-go uses one Quint model three ways: Quint traces drive the Go
code (`connect`), Hegel-driven Go runs are judged by the model
(`quinthegel`), and recorded production telemetry is replayed through it
(`validate`). The model must follow a few conventions for all three.

## Conventions (check every one)

1. **Name every action the implementation performs.** `step` must be an
   `any { ... }` of *named* actions. An inline `all { a, b }` inside `step`
   is an anonymous action: quint-connect cannot tell which code to run.
   Fix: `action aThenB = all { a, b }` and use `aThenB` in `step`.
2. **Parameters the code can observe.** Actions take as parameters what the
   implementation knows at that point (account, amount, batch id). Values
   the model chooses internally (a fresh id it allocates) are not
   parameters: the binding checks them with `expect` against what the code
   reported.
3. **`nondet` names = parameter names.** In `step`, pick each parameter with
   `nondet <param> = S.oneOf()` named exactly like the action parameter it
   feeds. `quint run --mbt` records picks by the `nondet` name, and both
   `connect.Pick[T](s, "<param>")` and `validate.ValidateITF` match them by
   name. Picks of other actions in the same step also appear (Quint records
   every `nondet` evaluated); ignore them.
4. **Parameterised module + instance module.** Put sizes and sets in
   `const`s and add an instance module for the deployed design:
   ```quint
   module bank { const ACCOUNTS: Set[str] ... }
   module bankDesign { import bank(ACCOUNTS = Set("alice", "bob")).* }
   ```
   `connect.Config{Spec: "bank.qnt", Main: "bankDesign"}` generates traces
   of the instance; a binding instantiates `bank` with constants sized to
   each recorded run (`constants:` templates such as `{{ max "amount" }}`).
   `-instance bankDesign` must be a single `import M(...).*`.
5. **Properties are boolean `val`s** (`val noOverdraft = ...`). They are
   `quint run --invariant` targets for the design, and `invariants:` of the
   binding for recorded runs.
6. **Restarts.** If the process can crash and restart, give the model one
   parameterless action for it (`action crash = ...`); the binding's
   `restart: crash` inserts it where the recorded process identity changes.
7. **Outcomes as values.** If an operation can fail in ways the design
   cares about, model the outcome as a parameter
   (`writeManifest(x, o: Outcome)` with `type Outcome = Ok | Fail | Ambiguous`)
   rather than leaving failures out.
8. **Hidden state is fine.** The model may have variables the code cannot
   observe (queues, other components); validation lets the model compute
   them. Only what a driver's `State()` returns, or a binding's `expect`
   mentions, is compared.
9. **Keep deterministic actions deterministic.** An action with a hidden
   `nondet` inside it (not a parameter) makes replay ambiguous: replay is a
   deterministic `run`. Lift the choice into a parameter.

## Workflow

```sh
quint typecheck model.qnt                                   # always first
quint run model.qnt --main design --invariant prop --max-steps 30 --max-samples 1000
quint run model.qnt --main design --mbt --max-steps 5 --n-traces 1 --out-itf /tmp/t.itf.json
# look at mbt::actionTaken and mbt::nondetPicks in /tmp/t.itf.json: every action
# named, every parameter picked under its own name
quint test model.qnt --main design                          # named `run ...Test` scenarios
quintgo scaffold -spec model.qnt -module M                  # binding skeleton from the signatures
```

Backends: `--backend rust` (default, fast) or `--backend typescript`.
quint-connect-go reads `QUINT_BACKEND` (or `QUINTGO_BACKEND`).

## Common errors

| Error | Cause | Fix |
|---|---|---|
| `an anonymous action was taken` | inline `all {}` in `step` | name it (convention 1) |
| `unknown nondet pick "x"` | the `nondet` is named differently from the parameter, or the action has no `nondet` for it | rename (convention 3) |
| `the trace has no mbt::actionTaken` | trace from `quint test` (no `--mbt`) | use `connect.Run`, or `NondetPath` for specs that record the action in state |
| `QNT500 Uninitialized const` | running the parameterised module | `--main` the instance module |
| `Cannot continue in "then"` (QNT513) in validation | the recorded step is not enabled in the model | an implementation bug, or a wrong binding; see the quint-trace-validation skill |

The spec is ground truth. When the code and the model disagree, decide from
the requirements which is wrong; never weaken the model just to make a test
pass without saying so.
