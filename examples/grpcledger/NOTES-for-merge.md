# Notes for merging examples/grpcledger

Fold these into the root docs, then delete this file.

## README example table row

| [examples/grpcledger](examples/grpcledger) | separate module: a gRPC ledger (idempotent transfers) with generated code, simulated through `simgrpc` (generated client and server unchanged; lost/duplicated requests, lost responses, refused connections, crashes) and served for real (otelgrpc spans converted to steps) | `NoDedupe` only with faults, `NoFundsCheck` from any load, `SplitTxn` (lost update) as a `Mismatch`; sweep of which faults expose the missing dedupe; Hegel shrink to one duplicated request |

## AGENTS.md

Layout line:

```
examples/grpcledger   separate module: gRPC ledger; simgrpc (any generated gRPC service in qenv: ServiceRegistrar + ClientConnInterface),
                      real tier over bufconn with otelgrpc spans converted to steps (ledger.convert.yaml); cmd/ledger-real
```

Requirements line:

- grpcledger (its own module): `cd examples/grpcledger && go test ./...`.
  Generated protobuf code is checked in; regenerate with buf (no protoc:
  see its README).

Gotchas:

- `simgrpc` delivers a response in the same scheduling step as the
  handler's return (it is a `qenv.Service`), so a crash never falls
  between a commit and its response. The `rpc.<name>.response` "lost"
  fault is how the simulation has a server that died after committing.
  Do not expect crashes alone to expose a missing idempotency check.
- `qenv.Store` has no deadlock detection. Two transactions that take
  `GetForUpdate` locks in opposite orders wait forever, and every request
  that needs those rows stalls behind them. The run records nothing, and
  Explore fails it as not conforming with an empty report. Lock rows in
  one order (grpcledger locks accounts by name).
- A step's observed values may be negative (an overdraft's balance): never
  use -1 as "not observed". Pass the observation or nothing.
- A missing dedupe shows two ways: a `transfer` the model cannot take
  (money moved twice), or a `refuse` of a key already applied (the
  duplicate found the funds gone). Hegel shrinks to the second. State the
  expectation as `AnyOf` of both.
- `quint` reserves `to` (and other built-in names): model parameters named
  `to` fail to parse (QNT101). grpcledger uses `src`/`dest`, while the
  proto keeps `from`/`to`.

## DESIGN section draft (§30: gRPC services, simulated and for real)

A gRPC service is checked the way checkout is, with one new seam:
`simgrpc` (examples/grpcledger/simgrpc, ~150 lines), which runs any
generated service inside qenv.
- `Server` implements `grpc.ServiceRegistrar`, so the generated
  `RegisterXServer` registers the real handlers. `Conn` implements
  `grpc.ClientConnInterface`, so `NewXClient` works unchanged.
- A call is protobuf-marshalled and crosses a `qenv.Service`, whose faults
  (dropped or duplicated request, lost response) and process crashes are
  qenv choices. simgrpc adds one kind of its own: a connection refused
  before sending.
- Transport outcomes become the status codes a real client sees:
  - no response in the connection's virtual Timeout: `DeadlineExceeded`,
    outcome unknown;
  - a full queue: `ResourceExhausted`;
  - a refused connection: `Unavailable`.
- Handler errors pass through as their status, and outgoing metadata
  arrives as incoming metadata.
- Streams, peers and the transport's own retries are not simulated. The
  client's retry policy lives in the client (`ledger.Client`), so both
  tiers run it.

The real tier serves the same handlers on a `grpc.Server` with otelgrpc.
At the commit, the server stamps its own span with the step
(`ledger.action`, fields, `ledger.seq` for commit order). The convert
rules (`ledger.convert.yaml`) then make the spans the steps, which is how a
production gRPC service would be checked from traces it already exports.
`quintgo validate -convert` checks them from the command line
(`run-demo.sh`).

What the example found:
- **The example's own first draft deadlocked.** Lock order was src, then
  dest; the faithful exploration's seed 22 recorded no steps. The
  simulation had found a real bug, but Explore said only "does not show
  the run conforms" with an empty report.
- **No crash window between commit and response.** A sweep row with
  crashes on and the network faults off conformed under `NoDedupe`.
- **Refusals are steps.** The model allows a refusal only when funds are
  short, so a ledger that wrongly refuses is caught too. (Checkout's open
  question, refused orders that record no step, is the same design point.)

Deviations from the brief:
- Unary RPCs only; NewStream returns `Unimplemented` in the simulation.
- The client's retry is in the client, not a gRPC service-config
  retryPolicy: the transport's retries cannot run over simgrpc, and a
  policy in code runs in both tiers.
- `SplitTxn` is checked in the simulation only. With MemDB's one lock per
  transaction, the real interleaving is a race the load rarely hits.
- No protoc on the machine: buf v1.50.0 compiles the proto, with
  protoc-gen-go v1.36.12 and protoc-gen-go-grpc v1.6.0. The generated
  code is checked in and the regeneration command reproduces it byte for
  byte.

## Suggested core follow-ups (not done: outside the example)

1. **Explore, a run with no steps:** say so ("seed 22 recorded no steps:
   N tasks were still waiting when the run ended"), instead of failing
   Conforms with an empty report. Better, `Sim.Settle` or `Close` could
   report tasks blocked with no deadline at the end, a deadlock detector.
2. **`qenv.Store` deadlock detection:** a cycle of `GetForUpdate` waits
   could abort one transaction with `ErrConflict`, as PostgreSQL's
   deadlock_detected does, so `qenv.Retry` recovers as with a real
   database. Today the stall is visible but unexplained. A real database
   would abort one side, so detecting the cycle is also more faithful.
3. **An opt-in crash window in `qenv.Service`:** a yield between the
   handler's return and the response's delivery would let a crash land
   after a commit. It changes the choice sequence, so it would have to be
   opt-in (`Service.CrashWindow`) to keep recorded traces replayable.
4. **simgrpc as a module:** it is generic, but grpc is too heavy for the
   core (OTel, yaml.v3 and hegel only), so a separate module (`qgrpc/`,
   like qweave) if a second gRPC example wants it.

## Skill text (quint-fault-injection, a new subsection near the doubles)

### gRPC services: `simgrpc` (examples/grpcledger)

```go
srv := simgrpc.NewServer(sim, "ledger", proc)            // a grpc.ServiceRegistrar in the simulation
ledgerpb.RegisterLedgerServer(srv, &ledger.Server{DB: store})
client := ledgerpb.NewLedgerClient(srv.Dial())           // a grpc.ClientConnInterface across qenv.Service
```

- The generated code is unchanged. Faults are `grpc.<name>.conn`
  (Unavailable before sending), `rpc.<name>.request`
  (drop/duplicate), `rpc.<name>.response` (lost), and crashes of
  `proc`. A missing response is `DeadlineExceeded` after `Conn.Timeout`
  of virtual time.
- Put the retry policy in client code (both tiers run it), not in a gRPC
  service config.
- Record steps in the handler at the commit (`Txn.OnCommit`). For the real
  tier, also set them as span attributes and convert the otelgrpc spans
  (`ledger.convert.yaml`, a commit-order attribute as `order:`).
- Lock rows in one order: `qenv.Store` does not detect deadlocks, and a
  deadlocked run records nothing.

## CI steps

```yaml
- name: grpcledger
  run: |
    cd examples/grpcledger
    test -z "$(gofmt -l .)"
    go vet ./...
    go test -short ./...           # ~80 s
- name: simulation lint (grpcledger)
  run: go run ./cmd/quintgo lint -sim examples/grpcledger
```

Measured timings, one machine, this module:
- `go test -short ./...`: 80 s.
- `go test ./...`: 432 s. Of that:
  - TestMutants: 132 s;
  - TestNoDedupeSweep: 124–180 s;
  - TestShrinkDoubleApply: 81–136 s, 34 runs checked with quint and 522
    from the verdict cache;
  - TestFaithfulConforms: 22–34 s;
  - real tier: 18 s.
