# examples/retrystorm

A client retry library (`retry/`): bounded retries, exponential backoff
with full jitter, and a retry budget (a token bucket that requests fill and
retries drain). It is checked at two levels:

- **Mechanism.** Does the budget do its accounting right? The budget
  records its steps (`qobs.Record`), and `model/retry.qnt` checks them. The
  model's invariants are the amplification bound (retries ≤ ratio ×
  requests) and the cap.
- **Effect.** Does a policy keep a loaded server out of a retry storm? This
  is a metastable failure: a transient spike pushes the system into a
  degraded state that persists after the spike is gone (Isaacs et al.,
  HotOS '25; Alvaro et al., 2025). Three tiers answer it, following the
  papers' pipeline:
  - `qmeta`'s CTMC predicts recovery times in seconds;
  - `qload`'s simulation, with the library itself deciding each retry on
    virtual time, confirms them;
  - Hegel searches for the smallest trigger that tips the system and
    shrinks it (`quinthegel.FindTrigger`).

The system is the papers' running example: one server at 10 requests/s
with a 100-request queue. Clients time out after 9 s and retry up to 3
times. The four policies compared are `immediate`, `backoff`, `budget`
(a retry per 10 requests, 10 kept) and `budget+backoff`.

## Tests

| Test | Tier | Shows |
|---|---|---|
| `TestBudgetConforms` | model: recorded `Burst` scenario | faithful budget conforms, `bounded` and `capped` hold |
| `TestBudgetCatchesUncapped` | model | `Uncapped`: mismatch at the request that overfills the bucket |
| `TestBudgetCatchesDepositOnRetry` | model | `DepositOnRetry`: mismatch at the first admitted retry |
| `TestHegelBudgetConforms` / `TestHegelCatchesUncapped` | model, Hegel bursts | faithful conforms; `Uncapped` found and shrunk |
| `TestPredictedRecovery` | CTMC | predicted recovery time per policy at 8, 9 and 9.5 requests/s |
| `TestStormAfterSpike` | simulation (seeds 1–6) | which policies recover from +5 requests/s for a minute at 9 requests/s |
| `TestFindTrigger` | Hegel over simulations | a trigger exists for `immediate` (at 9) and for `backoff` (at 8); none is found with a budget |

Results (recovery is from a full queue with 30 requests in the orbit):

| Policy | Predicted recovery at 8 / 9 / 9.5 req/s | Recover after the spike, at 9 req/s | Smallest trigger found |
|---|---|---|---|
| immediate | 2 600 s / 85 000 s / 820 000 s | 3/6 | +1.0 req/s for 3m36s (at 9 req/s) |
| backoff | 21 000 s / 1.4 M s / 22 M s | 0/6 | +2.0 req/s for 2m8s (at 8 req/s) |
| budget | 57 s / 139 s / 360 s | 6/6 | none in 30 cases |
| budget+backoff | 60 s / 154 s / 440 s | 6/6 | none in 30 cases |

What the tiers taught:

- **Backoff alone made recovery worse.** With a bounded queue, a request
  rejected at once and retried at once burns through its retries and gives
  up: rejection sheds load. With backoff, it waits and comes back, so the
  orbit stays full. The simulation showed this first. The first version of
  the chain predicted the opposite, for two reasons: it had no rejection
  semantics, and its orbit was bounded at 60. The chain now has both
  rejection rules (`qmeta.Params.Backoff`), and its orbit bound is 120.
  Check a chain's verdicts against the simulation before trusting it,
  especially near its bounds.
- **Only the budget removes the storm.** It caps the retry rate at a
  fraction of the request rate, so the orbit cannot feed itself.
- **The chain ranks policies; it does not time them.** Its absolute times
  are far larger than the simulation's. For example, immediate retries at
  9.5 req/s drain from (100, 30) in 3 to 18 minutes in simulation,
  against a predicted 9 days. The chain averages away timers and retry counts; for
  numbers, calibrate it on simulated trajectories (`qmeta.Calibrate`).
- **The model catches what load testing hardly sees.** With `Uncapped`
  and 6000 s of quiet before the spike, the bucket holds about 5000
  tokens, minutes of retries with no budget at all, and 4 of 5 runs still
  recover. The model rejects the first deposit that overfills the bucket.

## Running

```sh
go test ./examples/retrystorm/ -short -v      # ~40 s
go test ./examples/retrystorm/ -v             # + trigger search and shrinking, ~3 min
./run-demo.sh                                 # report (index.html, SVGs) + validate the budget's steps
MUTATION=uncapped ./run-demo.sh               # the validator rejects the run
```

The report has, per policy, the chain's field: arrows show the dynamics'
most likely direction over queue length and orbit. Next to it is one
simulated run through the spike.
