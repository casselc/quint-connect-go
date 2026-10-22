# A production workload against the simulation

Replay a captured cluster's activity while the system under test runs:
`kubesim.Fleet.Replay(s, trace, speed)` (examples/fleetrollout) applies a
SimKube trace's Deployment scalings to the simulated members, as another
writer (`kubesim.AsWriter`), through the same faults. The first replay
usually shows the model has no step for the environment's other writers.
Add one, deliberately (fleet.qnt `scale`), and explore with the workload
on.

Before a replay, lint the trace against the target:
`tr.Lint(sktrace.Env{Namespaces: []string{"dsb"}, Kinds: []string{"apps/v1.Deployment"}})`
lists the namespaces to create first, kinds the target does not serve,
owners applied after their dependants, and deletes of objects the trace
never applied (it started mid-run).

Measured pod times: `sktrace.Quantiles(tr.Lifetimes(""), 3)` are delay
options from shortest (nominal) to longest; scale them by the replay's
speed and give them to kubesim (`Cluster.PodTimes`, fleetrollout's
`Config.PodTimes`).
