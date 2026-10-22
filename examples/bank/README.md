# examples/bank

Every workflow of quint-connect-go on one small system. `bank.go` is an
in-memory bank with deposits, withdrawals and transfers. `model/bank.qnt`
is its Quint model, and `Mutation`s inject bugs. Each test file shows
which tests catch which bug.

| Test | Direction | Catches |
|---|---|---|
| `mbt_test.go` `TestModelDrivesBank` | the model drives (`connect.Run`), balances compared per step | `LostDeposit` |
| `mbt_test.go` `TestModelDrivenCatchesOnlyDivergence` | the same, against both mutants | only `LostDeposit`: the model never asks for a self-transfer |
| `hegel_test.go` `TestHegelRunsConformToModel` | Hegel drives, the model judges after the test (`After`) | — (faithful bank) |
| `hegel_test.go` `TestHegelCatchesSelfTransfer` | Hegel drives, the model judges each case, Hegel shrinks (`CheckCase`) | `SelfTransfer`, shrunk to a deposit and a self-transfer |
| `hegel_test.go` `TestHegelVariesModelTraces` | model traces replayed in Hegel, with drawn invalid requests | — (faithful bank) |
| `trace_test.go` `TestRecordedRun*` | steps recorded by `qobs.Record` in `bank.go`, checked with `qtest` | `LostDeposit` |
| `trace_test.go` `TestOTelSpansConvertedAndChecked` | the bank's plain OTel spans, converted by `bank.convert.yaml` | `LostDeposit` |
| `system_test.go` `TestTellersAsASystem` | three tellers in a qenv simulation as one `qtest.System`, the chooser picking operations and interleavings, explored by seeds | `SelfTransfer` (a rejected transfer) and `LostDeposit` (a mismatched deposit) |

Files:
- `bank.binding.yaml` maps recorded steps, and converted ones, to the
  model's actions.
- `bank.convert.yaml` maps the bank's OTel spans to steps.

```sh
go test ./examples/bank/ -short -v     # ~15 s
go test ./examples/bank/ -v            # + shrinking against the model, ~1-2 min
QUINT_SEED=0x1234 QUINT_VERBOSE=1 go test ./examples/bank/ -run ModelDrives -v
```

The same checks from the CLI, on a file of the bank's spans:

```sh
quintgo validate -binding bank.binding.yaml -convert bank.convert.yaml spans.jsonl
```
