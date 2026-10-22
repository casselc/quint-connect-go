# Services: HTTP, gRPC, streams

## HTTP servers (examples/kvstore)

kvstore is an HTTP service with CAS (`kvserver`, `kvload`): steps are
recorded in the handler where the write takes effect, or converted from
the server's OTel spans (`convert` rules), and validated with the
binding. Run it for real and validate its step log (`run-demo.sh`).

## gRPC services: `simgrpc` (examples/grpcledger)

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
- Lock rows in one order. `qenv.Store` detects a cycle of lock waits and
  aborts the transaction closing it (`qenv.ErrDeadlock`, an ErrConflict
  that `qenv.Retry` retries), as a real database does.
- `simgrpc.Server.CrashAfterCommit(true)` (`qenv.Service.CrashWindow`)
  lets a crash fall between a handler's commit and its response; without
  it only the lost-response fault stands for that.

## Streams: Kafka and Kinesis (examples/streams)

- **Streams (Kafka, Kinesis).** Seams: a `Writer` (Produce with a done
  callback, Flush) and a `Reader` (Poll, Commit). `examples/streams/simlog`
  is the simulated log:
  - named faults `log.produce` (lost, lostAck), `log.commit` (lost,
    lostAck) and `log.fetch` (rewind);
  - evictions as `rebalance.<consumer>` nemeses, with fencing;
  - an idempotent producer client.

  Record `read` at the consumer and let the model learn the log from it:
  the same binding then checks kfake (franz-go's in-process Kafka) and a
  fake Kinesis endpoint. A publisher should drain an outbox, so a restart
  resumes instead of redoing everything.
