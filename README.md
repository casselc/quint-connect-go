# quint-connect-go

Test Go code against [Quint](https://quint-lang.org) specifications.

quint-connect-go is the Go counterpart of
[quint-connect](https://github.com/quint-co/quint-connect) (model-based
testing, where Quint traces drive the implementation). It adds:
- **property-based and state-machine testing with
  [Hegel](https://github.com/hegeldev/hegel-go)**, judged and shrunk by
  the model;
- **trace validation**: Go programs report the model steps they take,
  from tests or production, through OpenTelemetry, a step log or their
  existing spans, and the model checks the recorded run.

```
                     ┌──────────── one Quint model ────────────┐
  connect            │  quint run --mbt ──► traces ──► Driver   │  the model drives Go (quint-connect)
  quinthegel.Replay  │  traces + Hegel draws ──► Driver         │  the model drives, Hegel varies the details
  quinthegel.Test    │  Hegel rules ──► Go ──► steps ──► model  │  Hegel drives Go, the model judges and shrinks
  qobs/convert/      │  running Go ──► steps / OTel / spans     │  the code reports, the model checks
  validate           │        ──► binding ──► quint test        │  (tests, CI, staging, production)
                     └──────────────────────────────────────────┘
```

Status: early (v0.x). The API may change. It is migrated from the
`quintgo` spike in casselc/observability, where it checks an OTel exporter
against its protocol model (see `examples/edgepublish`).

## Install

```sh
go get github.com/casselc/quint-connect-go@latest     # Go ≥ 1.26.1 (hegel-go's minimum)
npm install -g @informalsystems/quint                 # quint ≥ 0.32 on PATH
go install github.com/casselc/quint-connect-go/cmd/quintgo@latest   # optional CLI
```

Quint's Rust backend is the default. If quint cannot download its
evaluator, either run `ci/install-quint.sh` or set
`QUINT_BACKEND=typescript`. hegel-go embeds its engine, so it needs no
Python and no server.

## 1. The model drives Go (`connect`, like quint-connect)

```go
type driver struct{ bank *bank.Bank }

func (d *driver) Step(s *connect.Step) error {
	return connect.Switch{
		"init":    func(*connect.Step) error { d.bank = bank.New(accounts); return nil },
		"deposit": func(s *connect.Step) error {
			return d.bank.Deposit(ctx, connect.MustPick[string](s, "account"), connect.MustPick[int](s, "amount"))
		},
		// ... one case per model action; "_" is the catch-all
	}.Step(s)
}

// Compared with the model's state after every step (decoded with itf.Decode).
func (d *driver) State() (any, error) { return struct{ Balances map[string]int }{d.bank.Balances()}, nil }

func TestModelDrivesBank(t *testing.T) {
	connect.Run(t, connect.Config{Spec: "model/bank.qnt", Main: "bankDesign", MaxSamples: 30, MaxSteps: 25},
		func() connect.Driver { return &driver{} })
}
```

A divergence fails with the step, its picks, the model's next state, the
differing fields (`.Balances[bob]: model 16, implementation 17`), and
`reproduce with QUINT_SEED=0x…`.

| quint-connect (Rust) | quint-connect-go |
|---|---|
| `#[quint_run(spec, main, init, step, max_samples, max_steps, seed)]` | `connect.Run(t, connect.Config{...}, newDriver)` |
| `#[quint_test(spec, test, ...)]` | `connect.Test(t, cfg, "testName", newDriver)` |
| `impl Driver { fn step(&mut self, step: &Step) }` | `Driver.Step(*connect.Step) error` |
| `switch!(step { init => .., A(x, y?) => .. })` | `connect.Switch{"init": .., "A": ..}` with `Pick[T]` / `OptPick[T]` |
| `impl State<D>` (serde) | `State() (any, error)`, decoded by `itf.Decode` |
| `Config { state, nondet }` | `Config.StatePath`, `Config.NondetPath` |
| `QUINT_SEED`, `QUINT_VERBOSE` (compile time) | the same, read at run time, plus `QUINT_MAX_SAMPLES`, `QUINT_MAX_STEPS`, `QUINT_BACKEND`, `QUINT_BIN` |

Each trace gets a fresh driver. See
[skills/quint-mbt-driver](skills/quint-mbt-driver/SKILL.md) for the TDD
loop.

## 2. Hegel and the model (`quinthegel`)

**Hegel drives, the model judges.**
1. A Hegel state machine calls the code with drawn inputs, invalid ones
   included.
2. The model steps the code performs are recorded.
3. Each run must be a behaviour of the model, with the states the code
   reported and the model's invariants holding.

In `Each` mode a rejected run is a Hegel failure, so **Hegel shrinks it to
the shortest run the model rejects**. Verdicts are cached across
shrinking.

```go
type machine struct{ bank *bank.Bank }

func (m *machine) RuleTransfer(tc hegel.TestCase) {
	_ = m.bank.Transfer(ctx, hegel.Draw(tc, anyAccount), hegel.Draw(tc, anyAccount), hegel.Draw(tc, hegel.Integers(-1, 6)))
}

func TestBankAgainstModel(t *testing.T) {
	quinthegel.Test(t, quinthegel.Config{Binding: "bank.binding.yaml", Mode: quinthegel.Each, Global: true},
		func(ht *hegel.T, _ *quinthegel.Recorder) {
			hegel.RunStateful(ht, &machine{bank: bank.New(accounts)}, hegel.WithStatefulStepCount(12))
		}, hegel.WithTestCases(30))
}
```

Modes, measured on the same 40-case state machine (`quinthegel/modes_test.go`):

| Mode | How | All runs pass | A run fails |
|---|---|---|---|
| `Each` | check every case as it ends; Hegel shrinks failures against the model | 22 s | 32 s, shrunk |
| `After` | check all distinct runs at the end, in bulk | 3.3 s | 3.4 s, **not shrunk** |
| `Batched` | generate a round unchecked, check it in bulk, continue while it passes (`Rounds`, `Budget`); on a failure, replay the round in `Each` mode: cached verdicts decide every case up to the failing one, and Hegel shrinks it | 3.4 s | 23 s, shrunk |

Bulk checking (`validate.Checker.CheckBatch`) replays many runs per quint
process, grouped by instance. 24 runs take 1.6 s, against 6.6 s for one
process per run in parallel and about 14 s one at a time. Process
start-up dominates short replays. Bulk checking speeds up generation,
where runs are new; the verdict cache speeds up shrinking, where
candidates share prefixes. `Batched` uses both.

This is the direction that finds code accepting what the model forbids.
Model-driven tests cannot: the model never asks for a disabled step. In
`examples/bank`, a mutant that allows self-transfers passes every model
trace and keeps every balance non-negative, yet Hegel and the model shrink
it to `deposit(bob, 1); transfer(bob, bob, 1)` in about 50 quint checks.

**The model drives, Hegel varies.** `quinthegel.Replay` replays Quint
traces inside a Hegel test. Hegel picks the trace and the prefix, and the
driver draws what the model abstracts (payloads, timing, interleaved
invalid requests). Failures shrink to the simplest trace and the simplest
details.

See [skills/quint-hegel](skills/quint-hegel/SKILL.md) and
[DESIGN.md §9.2](docs/DESIGN.md).

## 3. The code reports, the model checks (`qobs`, `convert`, `validate`)

Instrument the code with model steps:

```go
qobs.Record(ctx, "deposit", err, "account", acct, "amount", n, "obs.balance", bal) // ~20 ns when off
```

or annotate functions (`//quint:action action:deposit account:acct amount:n`)
and build with [Orchestrion](https://github.com/DataDog/orchestrion). The
steps can go to OTel span events, a JSONL step log, or memory. Spans the
application already emits can be **converted** with a rule file instead.

A **binding** says how steps become model calls:

```yaml
spec: model/bank.qnt
module: bank
constants: { ACCOUNTS: 'Set("alice", "bob", "carol")', MAX_AMOUNT: '{{ atLeast 5 (max "amount") }}' }
actions:
  deposit:
    args: { account: '{{.account}}', amount: '{{.amount}}' }
    outcomes: { error: skip }                      # a refused call is no model step
    expect: 'balances.get({{.account}}) == {{.obs.balance}}'
invariants: [noOverdraft]
```

Then check, in a test or from files:

```go
qtest.RecordAndCheck(t, "bank.binding.yaml", func(*qobs.Recorder) { payday(bank.New(accounts)) })
```

```sh
quintgo validate -binding bank.binding.yaml steps.jsonl                          # step log, OTLP/JSON or ITF
quintgo validate -binding bank.binding.yaml -convert bank.convert.yaml spans.jsonl   # an app's plain spans
```

```
actor default: 10 steps
  conformance: FAIL
    after step [2] the model's state does not match what was observed
      quint:    deposit("carol", 2)
      expect:   balances.get("carol") == 4
      observed: seq 3 deposit(account=carol, amount=2)
      obs:      balance=4
```

Reconstruction orders steps from many recorders. It uses dense sequence
numbers (so a lost step is reported), program-order threads, domain
clocks, and process incarnations, which become the model's restart
action. See [docs/instrumentation.md](docs/instrumentation.md) and
[skills/quint-trace-validation](skills/quint-trace-validation/SKILL.md).

## Packages

| Package | |
|---|---|
| `connect` | MBT driver runtime: `Run`, `Test`, `Replay`, `Switch`, `Pick[T]`, `StateDriver`, `GenerateRun`/`GenerateTest` |
| `quinthegel` | Hegel integration: `Test` (modes `Each`/`After`/`Off`), `Recorder`, `CaseChecker`/`CheckCase`, `Replay`, `Traces` |
| `itf` | ITF traces: `Parse`, `Value`, `Decode`/`Encode` (Go ⇄ Quint values), `ParseQuint` (Quint literals) |
| `qobs` | runtime recorder: `Record`, `Thread`; sinks `MemorySink`, `FileSink`/`JSONLSink`, `OTelSink`, `Tee`; `orchestrion.yml` |
| `qtrace` | the step schema, `Reconstruct` (ordering, gaps, restarts), step log I/O |
| `otelio` | OTLP/JSON file exporter and reader; SDK spans to steps |
| `convert` | an application's own spans to steps, by YAML rules |
| `binding` | the YAML binding: lint against `quint typecheck`, annotation scan, plan, Quint module and ITF generation |
| `validate` | replay with `quint test`: `Checker.Check`, `CheckRuns`, `ValidateITF`, structured verdicts |
| `qtest` | record-then-check inside a Go test |
| `load` | read OTLP/JSON, a step log or ITF, detected by content |
| `skills` | the agent skills below, embedded; install targets |
| `cmd/quintgo` | CLI: `scaffold`, `lint`, `steps`, `validate`, `itf`, `convert`, `skills` |

## Example apps

Each one is a runnable Go app with a Quint model, a binding, mutants that
inject real bugs, a `run-demo.sh` and tests showing which check catches
which bug.

| App | What it is | What it shows |
|---|---|---|
| [`examples/glomers`](examples/glomers) | Fly.io's Gossip Glomers: unique ids, broadcast, grow-only counter, kafka-style log, on a Maelstrom runtime and simulator | distributed systems: models of each challenge with their properties; Lamport clocks to merge per-node step logs; Hegel scheduling deliveries, losses and partitions, and shrinking a lost-update bug to three steps; the same nodes under real Maelstrom, their logs conforming to the models |
| [`examples/kvstore`](examples/kvstore) | an HTTP key-value service with compare-and-swap (`kvserver`, `kvload`) | a concurrent service checked as a serial behaviour of its model, from its steps or its plain OTel spans (`kv.op` as a linearization key); a CAS race caught under load; errors that classify themselves; reads checked against the model; Hegel over HTTP |
| [`examples/jobqueue`](examples/jobqueue) | an at-least-once job queue with leases, workers and restarts (`jobqueue`) | all three verdicts: an implementation bug, a design property the run violates (`exactlyOnce`, checked with `-invariants`), and conformance; restarts inferred from incarnations; when model-driven testing does not fit |
| [`examples/bank`](examples/bank) | an in-memory bank (library) | every workflow on one small system: what model-driven testing cannot catch and Hegel + the model can |
| [`examples/edgepublish`](examples/edgepublish) | the spike's real case: an OTel exporter's publish protocol with S3 faults and crashes (own module, Orchestrion) | `//quint:action` annotations; three trace paths with identical verdicts; MBT with gated effects; model → Go → telemetry → model |

```sh
examples/glomers/maelstrom.sh                   # under real Maelstrom, then quintgo on the nodes' step logs
examples/kvstore/run-demo.sh                    # MUTATION=stale-cas | revision-on-conflict
examples/jobqueue/run-demo.sh                   # MUTATION=ack-any-lease | forget-done
```

## Agent skills and guidance

The skills in [`skills/`](skills) teach a coding agent this toolkit:
- `quint-model-for-go`: model conventions;
- `quint-mbt-driver`: drivers;
- `quint-trace-validation`: instrumentation, bindings and verdicts;
- `quint-hegel`: Hegel + Quint.

They are in the [Agent Skills](https://agentskills.io) format, one
`SKILL.md` per directory. In this repository they are exposed where agents
look for them: `.agents/skills/`, the vendor-neutral location read by
Cursor, VS Code/Copilot, Codex and others, and `.claude/skills/` for Claude
Code. Install them into your own project, or for your user:

```sh
quintgo skills install                     # .agents/skills and .claude/skills
quintgo skills install -target all         # also .github/skills and .cursor/skills
quintgo skills install -user               # under $HOME instead of the project
```

Repository guidance for agents is in [AGENTS.md](AGENTS.md); `CLAUDE.md`
imports it.

## Limits

- **Hidden nondeterminism.** A failure that may or may not have taken
  effect needs the implementation to know which (`QuintOutcome()`).
  Branching replay (Apalache) is not built.
- **Cost of trace checks.** Trace validation is post hoc and bounded by
  run length. Each check is one `quint test` process (~0.5–1 s).
- **`connect.Test`.** `quint test` traces carry no action annotations,
  so `connect.Test` needs `NondetPath`.
- **Conversion.** Converted spans cannot reveal lost spans.

More in [DESIGN.md](docs/DESIGN.md) §5 and §9.
