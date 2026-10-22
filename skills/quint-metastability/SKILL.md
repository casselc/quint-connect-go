---
name: quint-metastability
description: Find and prevent metastable failures (retry storms, a queue that stays full after a spike) in Go services and controllers with quint-connect-go's qload and qmeta packages - predict recovery with a CTMC (qmeta.New, HittingTimes, Field/SVG, Calibrate), confirm with a load simulation on virtual time (qload.Simulate, open- or closed-loop clients, the retry library under test as a qload.Retrier), search and shrink the smallest trigger with Hegel (quinthegel.FindTrigger, Recovers), compare mitigations (backoff, retry budgets, rate limits), and turn the calibrated chain into alerting rules over the application's own load roles (qrole: //quint:serve, //quint:operation, //quint:call; qmeta.Trajectories, CalibrateTimeout, Region, Rules). Use when choosing or reviewing a retry, timeout, backoff, requeue or rate-limit policy, when a system degrades after a load spike or a dependency slowdown and does not come back, or when a controller's requeue behaviour under a cloud API incident is in question.
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
  `qmeta.Mostly(sys, tr, n)` takes the majority, `Count` and `Runs`
  give the runs. A `Recovery` prints its verdict and a `Warning` when
  the goodput before the trigger was far below the offered load: an
  unhealthy baseline (set `Experiment.Healthy`).

### Measured service times

The chain assumes exponential service. Measure the handler alone (a
benchmark's latencies) with `qmeta.Measure(samples)`:
- `Mu()` gives the chain's rate;
- `CV` is 1 for exponential service;
- `Exponential()` says whether the chain's numbers can be trusted.

If not, simulate with the measured times (`qload.ServerConfig{Service:
svc.Draw}`). Constant service at the same mean made a full queue
absorbing: when the queue holds more than μ × timeout, every request in
it times out (retrystorm `TestServiceTimeShape`: 6 of 6 seeds recover
with exponential service, 0 of 6 with constant).

`qrole.Labels(true)` puts pprof labels on the roles, so a CPU profile
splits by server and service (`go tool pprof -tagfocus
quint.serve=api`).

### A production workload as the load

A SimKube trace (`sktrace.ReadFile`) is a real cluster's objects over
time. Its rates (`Rates`, `Starts`) or a gauge (`Gauge(tick,
sktrace.Replicas)`) become a load shape: `qload.Piecewise(time.Minute,
0.2, replicas...)` is 0.2 requests/s per desired replica, minute by
minute. Compare policies against a cluster's own scale-up, not only
synthetic spikes (examples/retrystorm `TestProductionScaleUp`).

## 2b. Or run the application itself (`qload.Experiment`)

qload's servers and clients are an abstraction. To put the real system
under load, give its dependency a capacity, keep everything else real,
and let the experiment drive it:

```go
x := qload.Experiment{
	Rate: 3, Healthy: 3, // or Population + Think for a closed loop (a controller's objects)
	Deploy: func(env *qload.Env) func(ctx context.Context, i int) {
		svc := qenv.NewService(env.Sim, "provider", nil, handler)
		rnd := env.Rand("provider.service")         // a seeded stream, not the Chooser
		svc.Capacity = &qenv.Capacity{Workers: 1, Queue: 50, Slowdown: env.Trigger.Factor(),
			Time: func() time.Duration { return time.Duration(rnd.ExpFloat64() / 5 * float64(time.Second)) }}
		env.Probe("backlog", queue.Pending)
		return func(ctx context.Context, i int) { app.PlaceOrder(ctx, i) }
	},
	Sink: mem, // optional: the same steps, to check against the model
}
r := qmeta.Assess(x, "markPaid", qmeta.Trigger{Slowdown: 3, Duration: time.Minute}, 5*time.Minute, seed)
ok, first := qmeta.Mostly(qmeta.Of(x, "markPaid", 5*time.Minute), trigger, 3)   // seeds in parallel
```

- **Goodput is a model step's rate.** The app's own recorded steps are
  counted per tick (`r.Series.Rate("markPaid", from, to)`), so
  performance and correctness come from one run:
  `qtest.Check(t, binding, mem.Steps(), qtest.Constants(...))`.
- **Set `Healthy`.** A system that collapsed before the trigger would
  otherwise "recover" to its collapsed goodput. With 8 consumers, the
  checkout shop collapses on its own, and that bug was hidden this way.
- **The verdict allows for noise.** Recovery means goodput is back to at
  least 90% of healthy, give or take two standard errors over the
  100-second window. At a few requests per second, a plain 90% threshold
  fails a healthy run one time in ten.
- **Faults in load runs.** `Experiment.Faults` injects them, and crashes
  are a rate per virtual second (`NemesisEvery`).

Look for loops around the retry loop. A broker's redelivery, a client's
retry or a controller's requeue each retries too. A budget on the inner
loop alone does nothing, as checkout's consumers showed.

## 3. Search: Hegel (`quinthegel.FindTrigger`)

```go
sys := qmeta.OfSimulation(cfg, base, 5*time.Minute) // or qmeta.Of(experiment, goodput, within)
r := quinthegel.FindTrigger(t, sys, qmeta.TriggerSpace{MaxExtraRate: 10, MaxSlowdown: 4,
	MaxDuration: 5 * time.Minute, Start: 10 * time.Minute}, hegel.WithTestCases(30)).Expect(t, true)
// r.Trigger is shrunk toward none
```

`Expect(t, true)` for a design that should be shown fragile,
`Expect(t, false)` for the mitigation (no trigger in the space breaks it). If it returns the empty trigger, the system
collapses on noise alone: look for a design bound, such as more
concurrency than capacity × timeout. Each case runs `Seeds` (default 3)
simulations, so keep it out of `-short`.

## 4. Alert: the chain's region over the app's own load roles (`qrole`)

Mark the roles in code, once, and the same gauges serve the simulation,
the calibration and production:

```go
//quint:serve name:api          // a handler: u, from arrival to response
func (s *Server) Handle(ctx context.Context, r Req) (Resp, error)

//quint:operation service:api   // all attempts and waits of one operation
func (c *Client) Do(ctx context.Context) error

//quint:call service:api        // one attempt
func (c *Client) attempt(ctx context.Context) error
```

- Weave with qweave (the tool file imports `qrole`), or call
  `qrole.Serve/Operation/Call` and defer the returned function with the
  error. v (the orbit) is operations minus calls in flight: no knowledge of
  the retry policy is needed.
- In a run, give each simulation its registry (`qrole.WithRoles`) and
  probe `roles.Queue(name)` and `roles.Orbit(service)` (`env.Probe`). In
  production, `roles.Instrument(meter)` exports the gauges
  (`quint.serve.inflight`, `quint.orbit`).
- Calibrate on the series: `qmeta.CalibrateTimeout(p,
  qmeta.Trajectories(obs, []float64{5, 10, 20}, 10))`. Hold λ (measured as
  the rate of new operations): fitting it to a storm drives it to μ, where
  nothing recovers.
- `region := qmeta.New(fit).Region(healthy, 120)` is the states with less
  than even odds of being healthy within 2 minutes (`RecoveryWithin`). Not
  the mean hitting time: near a storm it is dominated by rare tips into
  it, and alerts on states that drain in a second.
- `region.Rules(qmeta.RuleConfig{Server: "api", Service: "api"},
  "u <= 10")` is a Prometheus rule group, with the chain's parameters in
  its annotations: commit it, and a policy change shows up as its diff.
  No region (a budget): the rule cannot fire.
- `qrole.NewMetricsSink(meter, next)` counts every `//quint:action` by
  outcome (`quint.steps`): goodput and error ratio with no instrumentation
  of its own.

See examples/retrystorm (`alerts.go`, `TestAlertsFromTheChain`).

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
