# Fault points in your own code, and fault coverage

A double fails the way its kind of component fails. A fault specific to
your code (a client's own error, a crash between two writes) is a fault
point:

```go
//quint:fault kind:store.write error:ErrUnavailable error:ErrConflict crash:true
func (s *Store) Write(ctx context.Context, k, v string) error
```

- Weave with qweave, with `_ "github.com/casselc/quint-connect-go/qenv/fault"`
  in the tool file. Or call `qenv.FaultPoint(ctx, "store.write", true,
  ErrUnavailable)` at the top yourself.
- In a simulation the entry becomes the choice `fault.store.write`.
  Option 0 goes on. Each other option returns one of the named errors
  (the package's own sentinels), with zero values for the other results.
  `crash:true` adds a crash of the process there, restarted as the
  nemesis restarts it. With no error named, the fault is a
  `*qenv.InjectedError`. Outside a simulation the call returns nil.
- Rates apply as to any fault kind (`Kinds: {"fault.store": 0.2}`), and
  shrinking removes the faults the failure does not need.
- **Coverage.** `x := sys.Explore(t, cfg); t.Log(x.Coverage())`, or
  `qenv.CoverageOf(choices)` for recorded choices. It lists every fault
  kind, declared or a double's, with the runs that reached it and the
  options taken. A declared point no run reached (`Unreached`) is a
  failure path the exploration never tried, whatever the line coverage
  says. Raise its rate, or make a scenario reach it. `Pairs`, `Untried`
  and `OneWay` say which pairs of fault kinds ran together, and in which
  orders: fault-handling bugs are often two faults together.
