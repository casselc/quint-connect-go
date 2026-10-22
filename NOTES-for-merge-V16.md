# Notes for merging V16 (P7, finished)

Only skills/ changed. Apply the text below, then delete this file.

## What changed

- quint-metastability: SKILL.md keeps the tiers (predict, confirm, run
  the application, search, alert) and the findings; mechanics moved to
  references/simulation.md (Retrier contract, closed loops, triggers,
  measured service times, SimKube workloads), references/experiment.md
  (qload.Experiment) and references/alerts.md (qrole, calibration,
  Region, Rules, Watch, Profile).
- quint-trace-validation: references/emitting.md (step keys, outcomes,
  sinks), references/binding.md (template rules, lint, analyzers),
  references/production-logs.md (quintgo steps, pseudonyms) and
  references/patterns.md (the example apps' patterns).
- quint-hegel: references/modes.md (modes, Global, generators, budget)
  and references/pitfalls.md. The FindViolation example states its
  verdict with `v.Expect(t, qtest.Rejects("transfer"))` instead of
  inspecting `v.Last()`.
- quint-verification-workflow: "assert on `v.Summary`" replaced by
  `v.Expect`.
- quint-start: rows for gosim, weaving, Kubernetes controllers and
  fuzzing; a ten-line System snippet in examples/hello's shape.
- skills_test.go: TestSkillsAvoidDeprecated fails on qtest.Check(,
  qtest.RecordAndCheck, qtest.Explore(, qtest.SweepExplore, Explored and
  the old "step [i]" numbering anywhere in skills/.

## AGENTS.md

Conventions, after "`skills_test.go` checks every link resolves.":
"`skills_test.go` also fails on deprecated identifiers (qtest.Check,
RecordAndCheck, qtest.Explore(binding), SweepExplore, Explored, `step
[i]`): when deprecating an API, add it to `TestSkillsAvoidDeprecated`."

## docs/ROADMAP.md

V16: append "*Done:* the three skills split into references (simulation,
experiment, alerts; emitting, binding, production-logs, patterns; modes,
pitfalls); quint-start's new rows and System snippet; `v.Summary` advice
gone; `TestSkillsAvoidDeprecated`."
