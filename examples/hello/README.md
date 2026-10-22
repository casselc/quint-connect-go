# hello: the smallest simulated system

A counter service behind a network that loses requests, loses responses
and delivers some requests twice; a client that retries until it hears
back; a model that says every request counts once. The whole system is
[hello.go](hello.go) (80 lines). Start here before the bigger examples.

| file | what it is |
|---|---|
| [model/hello.qnt](model/hello.qnt) | the requirement: a request is applied once, and seen again only as a replay |
| [hello.binding.yaml](hello.binding.yaml) | the recorded steps as calls of the model's actions (`id` is the step's field of that name) |
| [hello.go](hello.go) | the service (`Counter.Add`, which records `apply` or `replay`), the retrying client, `Run` on a qenv simulation |
| [hello_test.go](hello_test.go) | one `qtest.System`, explored by seeds |

## What each test catches

| test | mutant | check | verdict |
|---|---|---|---|
| `TestFaithfulConforms` | none | 30 seeds, one message fault in five | every run conforms |
| `TestNoDedupeCountsRetries` | `NoDedupe`: a retried or duplicated request is counted again | the same exploration | some run's `apply` is rejected (the model has no second apply of a request); with no faults every run conforms: no retry, no bug |

The bug needs a fault to show: a lost response makes the client retry,
and a duplicate delivers the request twice. That is why the faults are
the environment's, chosen by the simulation, and every run is checked by
the model rather than by assertions written for the cases someone
thought of.

## Run it

```sh
go test .                       # needs quint on PATH
./run-demo.sh                   # simulate 20 seeds, check the step logs with quintgo
MUTATION=no-dedupe ./run-demo.sh
```

Next: the same System runs under Hegel (`quinthegel.Shrink(t, system(NoDedupe))`
shrinks to the one fault the bug needs), sweeps (`System.Sweep`) and the
fuzzer (`System.Fuzz`); [examples/checkout](../checkout) is a saga with
crashes, isolation levels and an outbox.
