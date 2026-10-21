---
name: quint-metastability
description: Find and prevent metastable failures (retry storms, a queue that stays full after a spike) in Go services and controllers with quint-connect-go's qload and qmeta packages - predict recovery with a CTMC (qmeta.New, HittingTimes, Field/SVG, Calibrate), confirm with a load simulation on virtual time (qload.Simulate, open- or closed-loop clients, the retry library under test as a qload.Retrier), search and shrink the smallest trigger with Hegel (qmeta.FindTrigger, Recovers), and compare mitigations (backoff, retry budgets, rate limits). Use when choosing or reviewing a retry, timeout, backoff, requeue or rate-limit policy, when a system degrades after a load spike or a dependency slowdown and does not come back, or when a controller's requeue behaviour under a cloud API incident is in question.
---

# Metastable failures with qload and qmeta

A metastable failure is not a wrong answer: it is a degraded state the
system cannot leave after its trigger is gone. A spike or a slowdown
fills a queue, and requests time out while the server still works on
them. Their retries keep the queue full. Correctness checks (Quint, qenv)
do not see it. These tools check the property that does:

> Within W after a transient stressor ends, goodput is back to at least
> 90% of what it was before.

Work in tiers, cheapest first, and let each check the next. This is the
pipeline of Alvaro et al. (2025) and Isaacs et al. (HotOS '25).

## 1. Predict: the CTMC (`qmeta`)

```go
p := qmeta.Params{Lambda: 9, Mu: 10, Workers: 1, Timeout: 9, Retries: 3, Queue: 100, Orbit: 120,
	Backoff: 0, RetryBudget: 0, RetryRate: 0} // mitigations, 0: none
c := qmeta.New(p)
h := c.HittingTimes(func(s qmeta.State) bool { return s.U <= 10 })
recovery := h[c.Index(qmeta.State{U: 100, V: 30})] // seconds, from a full queue
c.SVG(w, 10, 12)                                     // the field: where the dynamics go
```

- The state is (u, v): requests in the server, and requests in the orbit
  (their current attempt will time out, or they wait to retry).
- Recovery times that grow by orders of magnitude as λ rises mark the
  metastable region.
- Use the chain to rank policies and locate the tipping point. Do not use
  its absolute times: they can be off by orders of magnitude. For numbers,
  fit it to simulated trajectories with `qmeta.Calibrate`.
- Make the orbit bound (`Orbit`) generous. A chain truncated near the
  storm's size predicts recovery that isn't there.
- Mitigations: `Backoff` is the mean extra wait. `RetryBudget` caps
  retries per request, and `RetryRate` caps retries per second. Rejections
  at a full queue matter: an immediate retry burns its retries and gives
  up, while a retry after backoff comes back. The chain models both.

## 2. Confirm: the simulation (`qload`)

```go
cfg := qload.Config{
	Server: qload.ServerConfig{Name: "api", Workers: 4, ServiceRate: 5, QueueCap: 100},
	Client: qload.ClientConfig{Name: "c", Rate: qload.Constant(9), Timeout: 9 * time.Second, Retries: 3,
		Retrier: func(rnd *rand.Rand) qload.Retrier { return myPolicy(rnd) }}, // the library under test
}
r := qmeta.Recovers(cfg, 9, qmeta.Trigger{ExtraRate: 5, Duration: time.Minute}, 5*time.Minute)
```

- Put the real retry library behind `qload.Retrier`:
  `Request(now)` per new request and `Retry(n, now) (wait, ok)`. Any time
  it needs is the argument `now` (virtual), never `time.Now`;
  `x/time/rate` limiters take it (`ReserveN(now, 1).DelayFrom(now)`).
- Controllers are closed loops: set `Population` (objects) and `Think`
  (poll interval). An open-loop abstraction counts a failing object's
  polls on top of its retries and finds storms that cannot happen.
- Triggers: `ExtraRate` (load spike) or `Slowdown` (capacity dip, the
  realistic one for a dependency). Start them after the steady state
  (`Trigger.Start`; a closed loop needs one poll interval).
- Results are probabilities. Count recoveries over several seeds; in
  the metastable region, a run's escape is a coin toss.
  `RecoversMostly` takes the majority.

## 2b. Or run the application itself

qload is an abstraction. To put the real system under load, give the
dependency a capacity and keep everything else real:

```go
svc := qenv.NewService(s, "provider", nil, handler)
svc.Capacity = &qenv.Capacity{Workers: 1, Queue: 50, Shed: false,
	Time: func() time.Duration { return time.Duration(rnd.ExpFloat64() / 5 * float64(time.Second)) },
	Slowdown: func(t time.Duration) float64 { /* the trigger */ }}
```

Drive it with seeded open-loop customers and count goodput from the app's
own model steps, with a counting `qobs.Sink`. Wrap the run as a
`qmeta.System` for `qmeta.Mostly` and `qmeta.Search`. Record the same run
and check it with `qtest.Check`/`qtest.Validate`: recovery and
correctness are separate verdicts on one run. See examples/checkout
`load.go`.

Look for loops around the retry loop. A broker's redelivery, a client's
retry or a controller's requeue each retries too. A budget on the inner
loop alone does nothing, as checkout's consumers showed.

## 3. Search: Hegel (`qmeta.FindTrigger`)

```go
r := qmeta.FindTrigger(t, cfg, base, qmeta.TriggerSpace{MaxExtraRate: 10, MaxSlowdown: 4,
	MaxDuration: 5 * time.Minute, Start: 10 * time.Minute}, 5*time.Minute, hegel.WithTestCases(30))
// nil: no trigger in the space breaks it; else r.Trigger is shrunk toward none
```

Assert `r != nil` for a design that should be shown fragile, and
`r == nil` for the mitigation. `qmeta.Search(t, sys, space)` does the same
for any `qmeta.System`. If it returns the empty trigger, the system
collapses on noise alone: look for a design bound, such as more
concurrency than capacity × timeout. Each case runs `Seeds` (default 3)
simulations, so keep it out of `-short`.

## Correctness of the mechanism stays with Quint

A mitigation's own logic (a budget's accounting, a limiter's cap) is
ordinary code with ordinary bugs. Record its steps and check them against
a small model with the bound as an invariant (`retries*1000 <=
RATIO*requests`). A broken budget can pass every load test, because
storms are rare and short, while the model rejects its first wrong step.
See examples/retrystorm (`retry/`, `model/retry.qnt`).

## What the examples found

- examples/retrystorm, one server at 10/s with a 9 s timeout and 3
  retries:
  - immediate retries are metastable from about 9 req/s;
  - backoff alone made recovery worse;
  - a budget (a retry per 10 requests) recovered from every trigger
    tried.
- examples/crossdb (a Crossplane provider, 600 objects, a cloud API at
  20 calls/s):
  - crossplane-runtime's requeue (per-item backoff to 60 s, a global limit
    of 10 reconciles/s) recovers from capacity dips;
  - a fixed 1 s requeue on error does not;
  - the global limit alone saves it.
- examples/checkout (the saga itself, a provider at 5 captures/s with a
  1 s timeout):
  - 6 or more payment consumers collapse with no trigger;
  - a retry budget is bypassed by broker redelivery;
  - shedding expired calls at the provider recovers;
  - idempotency keys hold through the storm, and without them the storm
    alone charges twice.
