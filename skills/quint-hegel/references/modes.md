# Checking modes, recording, budgets

- **Modes:**
  - `Batched` is the usual choice: rounds of unchecked generation, bulk
    checks (many runs per quint process), and on a failure a replay of
    the round in `Each` mode, so Hegel shrinks it.
    - All passing, it costs about what `After` does (3.4 s against 22 s
      for `Each` on 40 cases).
    - Set `Rounds` or `Budget` to keep exploring, and `Seed` to
      reproduce.
    - The body must be deterministic in Hegel's draws.
  - `Each` checks every case, so a violation is a Hegel failure and Hegel
    shrinks it against the model. The checker caches verdicts, so
    shrinking runs quint far less than once per candidate.
  - `After` checks the distinct runs in bulk after Hegel finishes. It is
    the cheapest, but shrinks nothing.
  - `Off` records only.
- **`Global: true`** installs each case's recorder as the global qobs
  recorder too, for steps from goroutines the body starts (a server's
  handlers, a cluster's nodes). The body's own goroutine always records
  into the case. Tests with it must not run in parallel: a second install
  fails. Rules
  can also call `rec.Step(action, err, kv...)`. Every step becomes a Hegel
  note, so the shrunk run prints as model actions.
- **Generators.** Draw invalid inputs too: unknown ids, non-positive
  amounts, duplicates. Map refused calls to `outcomes: { error: skip }` in
  the binding. A refused call is then no model step, but a wrongly
  accepted one becomes a step the model rejects.
- **Budget.** Keep `WithTestCases` modest (20–50) and
  `WithStatefulStepCount` around 8–15 in `Each` mode. Skip the long
  shrinking tests under `testing.Short()`.

