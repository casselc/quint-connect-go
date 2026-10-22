# The application itself under load (`qload.Experiment`)


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
r := qload.Assess(x, "markPaid", qload.Trigger{Slowdown: 3, Duration: time.Minute}, 5*time.Minute, seed)
ok, first := qload.Mostly(qload.Of(x, "markPaid", 5*time.Minute), trigger, 3)   // seeds in parallel
```

- **Goodput is a model step's rate.** The app's own recorded steps are
  counted per tick (`r.Series.Rate("markPaid", from, to)`), so
  performance and correctness come from one run:
  `qtest.Validate(t, binding, mem.Steps(), qtest.Constants(...)).Expect(t, qtest.Conforms())`.
- **Set `Healthy`.** Without it `Assess` is not a verdict
  (`Recovery.Err`): a system that collapsed before the trigger would
  "recover" to its collapsed goodput. With 8 consumers, the checkout shop
  collapses on its own, and that bug was hidden this way. State the
  verdict with `r.Expect(t, true)` (or `false` for a storm you want to
  see).
- **The verdict allows for noise.** Recovery means goodput is back to at
  least 90% of healthy, give or take two standard errors over the
  100-second window. At a few requests per second, a plain 90% threshold
  fails a healthy run one time in ten.
- **Faults in load runs.** `Experiment.Faults` injects them, and crashes
  are a rate per virtual second (`NemesisEvery`).

Look for loops around the retry loop. A broker's redelivery, a client's
retry or a controller's requeue each retries too. A budget on the inner
loop alone does nothing, as checkout's consumers showed.

