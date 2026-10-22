---
name: quint-start
description: Start here for checking Go code against a Quint model with quint-connect-go - a question-to-tool table that picks the workflow (model-based tests with connect, Hegel with quinthegel, trace validation with qobs/binding/validate, simulated fault injection with qenv and qtest.System, metastability with qload/qmeta, coverage, mutants, sweeps, fuzzing, Choreo models) and the skill to load next. Use when a task mentions Quint and Go together and it is not yet clear which part of the toolkit applies, or when asked what the toolkit can do.
---

# quint-connect-go: which tool for which question

Find the question, then load the skill it names; each skill loads its own
references when the task needs them.

| Question | Tool | Skill |
|---|---|---|
| I have Go code and no model yet | write the model first, simulate its invariants | quint-model-for-go, quint-modeling-patterns |
| What should the model allow? Why did a buggy system pass? | patterns: requirement vs solution, the environment's real guarantees | quint-modeling-patterns |
| Does my code do what the model's traces do? | `connect.Run` (model drives the code, state compared) | quint-mbt-driver |
| Does the code accept inputs the model forbids? | Hegel drives, the model judges (`quinthegel.Test`, `FindViolation`) | quint-hegel |
| Do runs of the real system (tests, staging, production) conform? | record steps (`qobs`, `//quint:action`, OTel spans, `convert`), bind, `quintgo validate` | quint-trace-validation |
| Is it correct under crashes, lost responses, redelivery, isolation levels? | `qenv` simulation, `qtest.System` (`Explore`, `Sweep`, `Fuzz`), `quinthegel.Shrink` | quint-fault-injection |
| Which code did no run reach, and does the model constrain the code that ran? | `qweave -cover`, `qweave -mutate`, `System.Mutate`, `x.Evidence()` | quint-fault-injection (its coverage reference) |
| Does it recover after a load spike or a slow dependency? | `qmeta` chain, `qload` simulation, `quinthegel.FindTrigger`, alert rules | quint-metastability |
| My system is an HTTP server, a gRPC service, or Kafka/Kinesis consumers and publishers | the seams and examples: kvstore, grpcledger (`simgrpc`), streams (a simulated log, kfake) | quint-fault-injection (its services reference) |
| My model is written in Choreo | name each transition (`choreo::step_with`), then any workflow | quint-model-for-go (Choreo section) |
| I am starting a component or an example, or setting up CI | the end-to-end workflow and triage | quint-verification-workflow |
| A test failed: whose bug is it? | the verdict table and triage | quint-verification-workflow (its verdicts reference) |

## First commands

```sh
quint typecheck model.qnt
quintgo scaffold -spec model.qnt -module M      # a binding skeleton
quintgo scaffold -system -binding m.binding.yaml -pkg app > system_test.go   # a qtest.System test to fill in
quintgo lint -binding m.binding.yaml -src .     # binding vs model, annotations vs binding
quintgo lint -sim .                             # code that escapes a simulation
quintgo validate -noitf -binding m.binding.yaml run.jsonl
```

The smallest simulated example is `examples/hello` (a counter, a lossy
network, a retrying client, one mutant): read it before the bigger ones.

In Go tests, `qtest.RequireQuint(t)` skips without quint; state what
each test expects with `qtest.Expect` and its claims, never by matching
report text.
