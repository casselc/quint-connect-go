# Confirm with a load simulation (`qload`)


```go
cfg := qload.SimConfig{
	Server: qload.ServerConfig{Name: "api", Workers: 4, ServiceRate: 5, QueueCap: 100},
	Client: qload.ClientConfig{Name: "c", Rate: qload.Constant(9), Timeout: 9 * time.Second, Retries: 3,
		Retrier: func(rnd *rand.Rand) qload.Retrier { return myPolicy(rnd) }}, // the library under test
}
r := qload.Recovers(cfg, 9, qload.Trigger{ExtraRate: 5, Duration: time.Minute}, 5*time.Minute)
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
  `qload.Mostly(sys, tr, n)` takes the majority, `Count` and `Runs`
  give the runs. A `Recovery` prints its verdict and a `Warning` when
  the goodput before the trigger was far below the offered load: an
  unhealthy baseline (set `Experiment.Healthy`).

## Measured service times

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

## A production workload as the load

A SimKube trace (`sktrace.ReadFile`) is a real cluster's objects over
time. Its rates (`Rates`, `Starts`) or a gauge (`Gauge(tick,
sktrace.Replicas)`) become a load shape: `qload.Piecewise(time.Minute,
0.2, replicas...)` is 0.2 requests/s per desired replica, minute by
minute. Compare policies against a cluster's own scale-up, not only
synthetic spikes (examples/retrystorm `TestProductionScaleUp`).

