# Alerts from the chain over the app's own load roles (`qrole`)


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
  nothing recovers. `fit.Warning()` says when the fit got there anyway.
- `roles.Orbit` is the waiting part of the chain's v (requests between
  attempts); the chain's own v also holds attempts in flight that will
  time out, known only in hindsight (`qload.Sample.Orbit` vs `Waiting`).
  Under immediate retries the gauge stays at 0 through a storm: the
  alert sees u alone there.
- `qmeta.Watch{Region, State, Snapshot}` runs in production next to the
  rule: once (u, v) has been in the region `For` samples, it writes the
  runtime's flight recorder (the last seconds of execution trace) to
  `Snapshot`; `go tool trace` shows what every goroutine was doing as the
  storm began.
- Where a simulated storm's latency goes: set `env.Sim.Profile =
  qenv.NewProfile()` in `Deploy`, then `Profile.WriteTo` a file and
  `go tool pprof -top -cum` it: each wait at a seam, by stack, weighted by
  virtual time (`-tagfocus task=<kind>` for one kind of task).
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

