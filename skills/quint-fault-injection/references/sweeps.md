# Sweep the configuration space: covering arrays (`qsweep`, `System.Sweep`)

When the product of configurations grows (isolation level × sizes ×
which fault kinds happen), sweep pairwise rows instead of the product:

```go
space := []qsweep.Factor{{Name: "iso", Values: []string{"ser", "si", "rc"}}, qsweep.Ints("stock", 1, 2, 3).Constant("STOCK"),
	qsweep.Bool("crash"), qsweep.FaultKind("rpc.provider")}
sys := qtest.System{Binding: "app.binding.yaml", Run: func(c qenv.Chooser) error {
	row, _ := qtest.RowOf(c) // the sweep's row; outside a sweep, ok is false
	return app.Run(c, cfgOf(row))
}}
sys.Sweep(t, qtest.SweepConfig{Factors: space, Explore: qtest.ExploreConfig{Seeds: 20},
	Expect: func(r qsweep.Row) []qtest.Claim { ... }}) // nil: Conforms
```

- The System's own `Run` serves the sweep: it reads the row from its
  chooser (`qtest.RowOf`), so one Run serves Explore, Shrink and Sweep.
- A `qsweep.FaultKind` factor switches a fault kind: in a row where it
  is off, the chooser already has the kind's faults off (`qenv.Without`).
- Every pair of factor values is in some row (`Strength: 3` for
  triples), in about as many rows as the two largest factors' product.
  `Allow` rules rows out, and pairs only those rows have are not
  required.
- Fault kinds as `Bool` factors, turned off with `qenv.Without`, give
  swarm testing with a guarantee: every pair of kinds runs together, and
  each without the others masking it.
- A row's sizes need the model's matching constants: name the constant
  on the factor (`.Constant("STOCK")`) and each row is checked with its
  value. When every run fails at step 1, the report names the binding's
  constants: the usual cause.
- Expect per row: say where a mutant's bug is real. Checkout's
  `NoIdempotencyKey` sweep splits it into its two bugs: one in every row,
  and one only where the provider's responses are lost: rows without
  provider faults claim `qtest.Violates("settledCorrectly")` and
  `qtest.None(qtest.Violates("chargedAtMostOnce"))`.
- Orders of faults: a `qenv.Script` per ordered pair (`{a, b}` with b's
  `Follows: []int{0}`) imposes both orders of every pair, and
  `Remaining` names an order the system cannot produce. Without
  `Follows`, faults of different kinds are independent (each kind's in
  order): a fault whose phase has passed blocks no other.
  `qsweep.Sequences` (every order of t events in few sequences) fits
  only events that recur through a run, such as crashes and partitions.
