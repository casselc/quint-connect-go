# Notes for merging V14 (lints that see more)

Root docs were not edited in this branch. Apply the text below, then delete
this file.

## What changed

- `qlint.Sim` (`quintgo lint -sim`) scans `_test.go` files too: a
  System's `Run` closure lives in a test. `//qenv:ok` exempts a line as
  before. `qlint.Woven` (`-weave`) still skips tests (unchanged).
- The 19 findings that change surfaced in the root module were all
  deliberate and are marked `//qenv:ok <reason>`:
  - `examples/checkout/synctest_test.go` (2): the synctest bubble's clock.
  - `qenv/qenv_test.go` (13): real time measured to show sleeps were
    virtual or to bound the scheduler's cost; parallel simulations each
    started from its own goroutine; `context.Background()` steps that test
    the goroutine fallback and the strict-mode panic.
  - `qenv/weave/weave_test.go` (1): `go weave.Go(...)()`, what weaving makes
    of a `//qenv:go` statement.
  - `qtest/cache_test.go` (1), `qtest/system_test.go` (2): goroutines that
    run an exploration with `failTB`, whose `Fatalf` ends the goroutine.
  - grpcledger and streams: no findings.
- `binding.ScanRecords(dir)` / `binding.RecordsOf(fset, f)` (std-only)
  find `qobs.Record(ctx, "a", ...)` and `x.Record(ctx, "a", ...)` calls
  with a literal action, in files importing qobs (a call on another
  imported package, like `qtest.Record(t, "b.yaml", ...)`, is not one).
  `binding.LintRecords(m, recs)` reports actions the model lacks, with the
  nearest action ("did you mean") and the model's actions.
  `(*Binding).LintSource(m, anns, recs)` checks annotations and records
  together, so a bound action a literal Record emits is no longer warned
  as emitted by nothing. An unknown annotation action now gets the same
  did-you-mean and list (the message keeps its "X is not an action of M"
  prefix).
- `quintgo lint -binding b.yaml -src DIR` scans literal Record actions
  too (prints "N literal Record calls under DIR") and fails on unknown
  ones.
- qanalysis: quintsim checks test files (as the CLI does); quintaction
  with `-binding` checks literal actions of `qobs.Record` and
  `(*qobs.Recorder).Record`, using the same scan confirmed by types (a
  type's own `Record` method is not reported). The analysistest stub qobs
  gained `Recorder.Record`.
- Tests: `qlint/lint_test.go` (a test file is scanned, `//qenv:ok` exempts
  in it), `binding/records_test.go` (scan, did-you-mean, LintSource),
  `cmd/quintgo/lint_test.go` (lint -binding -src, needs quint),
  `qanalysis` fixtures `simapp/sim_test.go` and `bindingapp/record.go`.
- Running every example's binding with `-src` its directory found no
  unknown Record literal. edgepublish's `probe` annotation (`woven`) is
  reported as before (the weaving probe; left as is).

## README.md

In the paragraph ending "which `quintgo lint -sim DIR` flags, along with
wall-clock time.", append: "Tests are checked too (a System's `Run`
closure lives in one); mark a deliberate real goroutine or clock read
`//qenv:ok <why>`."

Package table, `qlint` row: "the simulation and weaving lints (`quintgo
lint -sim`, tests included; `-weave`), per file for the analyzers".

Package table, `binding` row (append): "; `ScanRecords`/`LintRecords`:
literal `qobs.Record` actions checked against the model".

## AGENTS.md

In the gotcha "Simulated runs record into their own recorder ...", change
the last sentence to: "`quintgo lint -sim .` (in CI) flags `go`
statements and wall-clock calls in files that import qenv, tests
included; `//qenv:ok` exempts a line (give the reason after it)."

Add a gotcha:

- `quintgo lint -binding -src` and quintaction (`-binding`) check literal
  actions passed to `qobs.Record` and `Recorder.Record`, in files that
  import qobs. An action through a constant or variable is not seen, and
  a call on another imported package's `Record` (qtest's) is skipped by
  its name; the analyzer confirms each call by type, the CLI cannot.

## docs/DESIGN.md

In the qanalysis section (after "With `-binding` it runs
`binding.LintAnnotations` per annotation ..."), add:

> It also checks explicit steps: the literal action of every
> `qobs.Record` and `(*qobs.Recorder).Record` call must be one of the
> model's (`binding.LintRecords`, with the nearest action and the list).
> The scan is the binding package's (`binding.RecordsOf`, syntax only, so
> the CLI shares it); the analyzer confirms each call is qobs's by type,
> which the CLI approximates by skipping calls on other imported
> packages. quintsim checks test files, like `quintgo lint -sim`: a
> System's `Run` closure is test code, and a `go` statement there escapes
> the run as surely as one in the library.

## skills

`skills/quint-trace-validation/SKILL.md`, replace "It checks the binding
against `quint typecheck` and the annotations against the binding." with:
"It checks the binding against `quint typecheck`, the annotations against
the binding, and the literal action of every `qobs.Record` call against
the model (a misspelled action gets a did-you-mean)."

`skills/quint-fault-injection/SKILL.md` (where `quintgo lint -sim` is
mentioned): "It checks tests too, where a System's `Run` closure lives;
mark a deliberate real goroutine or clock read `//qenv:ok <why>`."

## docs/ROADMAP.md

V14: append "**Done.**" (lint -sim scans tests; literal Record actions
checked by `quintgo lint -binding -src` and quintaction).
