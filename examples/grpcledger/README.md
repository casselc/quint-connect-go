# grpcledger: a gRPC service, simulated and for real

A ledger served over gRPC (`proto/ledger.proto`): `Transfer` moves money
between three accounts, idempotent by key, and `Balance` reads one. The
model (`model/ledger.qnt`) is what a client relies on: a key applies once,
only with the funds there, and money is conserved. A refusal is a step too,
allowed only when the funds are short.

The same server code (`ledger/`) runs in two tiers.

- **Simulated** (`grpcledger.go`, `simgrpc/`): the generated client and
  server, unchanged, connected by `simgrpc`. `simgrpc.Server` is a
  `grpc.ServiceRegistrar` and `simgrpc.Conn` a `grpc.ClientConnInterface`,
  so `RegisterLedgerServer` and `NewLedgerClient` take them. A call crosses
  a `qenv.Service` and is marshalled with protobuf. Every fault is a qenv
  choice:
  - `grpc.ledger.conn`: unavailable before sending;
  - `rpc.ledger.request`: dropped or duplicated;
  - `rpc.ledger.response`: lost after the handler ran;
  - a crash of the server's process.

  The store is a `qenv.Store`. Seeds explore all this, a covering array
  sweeps which faults are on, and Hegel shrinks a failure to the one fault
  it needs.
- **Real** (`real.go`, `cmd/ledger-real`): a `grpc.Server` with otelgrpc
  over bufconn, an in-memory store (`ledger.MemDB`), and a client
  interceptor that refuses calls and loses responses. The server stamps
  each request's span with its step at the commit (`ledger.action`,
  `ledger.seq`). `ledger.convert.yaml` turns the spans into steps, as it
  would a production service's exported traces.

Steps are recorded at the server's commit (`Txn.OnCommit`), never by the
client on return. A transfer whose response was lost still happened.

## Mutants and the checks that catch them

| Mutant | What is wrong | Caught by |
|---|---|---|
| `NoDedupe` | the idempotency key is never looked up | needs faults: a duplicated request or a lost response's retry runs the key again, a `transfer` or `refuse` the model rejects. Without faults it conforms. |
| `NoFundsCheck` | money moves without the funds | any load that overdraws: a `transfer` the model rejects |
| `SplitTxn` | balances read in one transaction, written in another | an interleaving, no faults: a `transfer` whose balances differ from the model's (`Mismatch`) |

## Tests

| Test | Tier | Shows |
|---|---|---|
| `TestFaithfulConforms` | simulated | 60 seeds with crashes and every network fault conform; `Evidence` lists the 4 fault kinds faulted and every pair together |
| `TestMutants` | simulated | the table above: `NoDedupe` conforms without faults and is caught with them; the others need none |
| `TestNoDedupeSweep` | simulated | pairwise over {conn, request, response, crash}: the bug shows exactly in rows with duplicated requests or lost responses |
| `TestCrashAfterCommit` | simulated, crashes only | with a crash window after the commit, crashes alone expose `NoDedupe`; the faithful ledger conforms |
| `TestShrinkDoubleApply` | simulated, Hegel | shrinks `NoDedupe` to one duplicated request (`quinthegel.Shrink`), replayable from its choices |
| `TestRealSpansConform` | real | a concurrent load with refused calls and lost responses: the otelgrpc spans conform |
| `TestRealMutants` | real | `NoDedupe` (lost responses) and `NoFundsCheck` caught from spans |

`go test -short ./...` runs a fifth of the seeds and skips the sweep and
the shrink (about 80 s, most of it quint). `./run-demo.sh` serves the
ledger for real and validates its spans with `quintgo validate -convert`
(`MUTATION=no-dedupe` to see a failure).

## What the simulation found in this example

- **A deadlock in the first draft.** Transfers locked `src`, then `dest`,
  so a→b and b→a could each hold one lock and wait forever. Every later
  request then stalled behind them. The run recorded no step at all, and
  Explore failed it as not conforming (a report with no actors). The
  server now locks accounts in name order. qenv's store now detects the
  cycle and aborts one side with `qenv.ErrDeadlock`, as a real database
  does, so the same bug would show as retries instead of a silent stall.
- **A crash could not lose a committed response.** `qenv.Service`
  delivered a response in the handler's scheduling step, so only the
  lost-response fault stood for "the server died after committing" (the
  sweep's expectations, whose crash rows run without it, still say so).
  `qenv.Service.CrashWindow` (here `Config.CrashAfterCommit`) opens that
  window: `TestCrashAfterCommit` shows crashes alone exposing the missing
  dedupe.

## Regenerating the protobuf code

```sh
go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.12
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.0
go run github.com/bufbuild/buf/cmd/buf@v1.50.0 generate   # in this directory; no protoc needed
```
