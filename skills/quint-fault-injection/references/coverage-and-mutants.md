# Code coverage, one report, and mutants

## Which code each step ran: `qweave -cover`

```sh
go run -C path/to/qweave ./cmd/qweave -C . -cover [-coverpkg your/module/pkg] go test -run TestX ./...
```

```go
x := sys.Explore(t, qtest.ExploreConfig{Seeds: 30, Coverage: true}) // sys is a qtest.System
t.Log(x.CodeCoverage()) // never run, run by no step, outcomes that ran the same code
```

- Skip the test when `len(qcover.Blocks()) == 0` (not woven).
- "Never run" after a conforming exploration: an error path whose fault
  was never taken (check `x.Coverage()`), code the model's environment
  rules out, dead code, or a scenario the model does not describe.
- "Run by no step" is code no step of its own task explains: clients
  whose steps the services record, relays, setup, or work after a task's
  last step. A worker's code there means a missing step.
- Two outcomes of an action that ran the same code: the model tells them
  apart and the code does not. That is fine when the outcome is the
  environment's (a lost response), and a question when it is the code's.

## A real environment's coverage against the simulation's

Build the real environment's binaries with `go build -cover`, run them
with `GOCOVERDIR=dir`, and compare with a woven simulation's summary
(write it as JSON from `x.CodeCoverage()`):

```sh
quintgo cover -covdir run1,run2 -sim summary.json
```

"Real only" is code the simulation never leads to (a fault its doubles
do not inject, a path they do not take): its verdicts say nothing about
it. "Simulation only" is what the real runs did not exercise. Go's
counters are per process, so the real side has no per-step attribution.

## Dead code, and error paths never tried

`actionmap` (qanalysis) has the call graph: `go run ./cmd/actionmap ./...`
lists dead functions (no main, init or exported API reaches them). With a
summary and the runs' fault coverage, `actionmap.UntriedErrorPaths(m, s,
x.Coverage())` lists the blocks no run ran in the callers of a fault point
the runs reached but never faulted: its error handling. Untried, not dead:
raise the kind's rate or script it first.

## One report: `x.Evidence()`

`t.Log(x.Evidence())` (an exploration, or a sweep's `Swept`) says in a few
lines what the runs show: verdicts by reason, fault kinds faulted and
pairs together (and those never faulted), the code run (with
`Coverage: true`), and mutants killed (set `Mutants` from
`sys.Mutate`). Read it after a green test: it is what "conforms" leaves
unsaid.

## What the model constrains: mutants (`qweave -mutate`, `System.Mutate`)

```sh
go run -C path/to/qweave ./cmd/qweave -C . -mutate -coverpkg your/module/pkg go test -run TestMutation ./...
```

```go
rep := sys.Mutate(t, qtest.ExploreConfig{Seeds: 20, Rates: crashy}, nil) // skips unwoven
t.Log(rep) // reached, killed, survived, unreached
```

- Every mutant (a negated `if`, a moved comparison boundary, a dropped
  call) is in one build; each is explored with the seeds, verdicts shared.
- A survivor is code the model does not constrain. Ask, for each: is it
  equivalent (a dropped ack the environment redelivers anyway), is the
  effect unobserved (a step records nothing the model compares: add the
  field and an `expect` to the binding), or is the code not needed?
- Checkout's first run had a cancel that forgot to restock survive: no
  step carried the stock. Recording it and an `expect: 'stock ==
  {{.obs.stock}}'` killed it.
- Unreached mutants are coverage's: no run reached their code.
