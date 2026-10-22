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
  existing spans, and the model checks the recorded run;
- **simulated environments with fault injection** (`qenv`): the system
  runs deterministically against a store, queues and networks that do what
  their contracts allow (lost responses, redeliveries, crashes, an
  isolation level's anomalies), every choice made by Hegel or a seed, and
  every run checked by the model;
- **metastable failures** (`qload`, `qmeta`): whether a system recovers
  after a load spike or a capacity dip, or stays stuck in a retry storm.
  A CTMC predicts it, a simulation confirms it, and Hegel finds the
  smallest trigger.

```
                     ┌──────────── one Quint model ────────────┐
  connect            │  quint run --mbt ──► traces ──► Driver   │  the model drives Go (quint-connect)
  quinthegel.Replay  │  traces + Hegel draws ──► Driver         │  the model drives, Hegel varies the details
  quinthegel.Test    │  Hegel rules ──► Go ──► steps ──► model  │  Hegel drives Go, the model judges and shrinks
  qobs/convert/      │  running Go ──► steps / OTel / spans     │  the code reports, the model checks
  validate           │        ──► binding ──► quint test        │  (tests, CI, staging, production)
  qenv + qtest.      │  Go in a simulated environment, faults   │  Hegel or seeds choose schedules and faults,
  Explore            │  chosen ──► steps ──► model              │  the model judges every run
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
  deposit:                                         # account, amount: the step fields of those names
    outcomes: { error: skip }                      # a refused call is no model step
    expect: 'balances.get({{.account}}) == {{.obs.balance}}'
invariants: [noOverdraft]
```

Then check, in a test or from files:

```go
qtest.RecordAndCheck(t, "bank.binding.yaml", func(*qobs.Recorder) { payday(bank.New(accounts)) })
// a mutant, and how it must show up: Conforms, Rejects(action), Mismatch(action), Violates(invariant)
qtest.Record(t, "bank.binding.yaml", func(*qobs.Recorder) { payday(bank.New(accounts, bank.WithMutation(bank.LostDeposit))) }).Expect(t, qtest.Mismatch("deposit"))
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

## 4. Simulated environments and fault injection (`qenv`)

A fault is a choice the environment's contract allows. A response can be
lost after the request took effect, a message redelivered, a read served
from a snapshot. `qenv` runs the system's tasks one at a time and switches
between them only at seams. One `Chooser` makes every choice:
- `quinthegel.Chooser(tc)`: Hegel explores and shrinks;
- `qenv.NewSeeded(seed)`: random runs at per-kind fault rates;
- `qenv.Replay`: repeats a recorded run;
- `qenv.Nominal`: no faults.

Option 0 of every choice is nominal, so shrinking removes faults.

```go
s := qenv.New(chooser)
db := qenv.NewStore(s, "shop", qenv.SnapshotIsolation)   // or Serializable, ReadCommitted
q := qenv.NewQueue(s, "payments")                        // at least once: lost acks, redelivery, reordering
pay := qenv.NewService(s, "provider", nil, handler)      // lost or duplicated requests, lost responses
s.Process("payments", true, func(ctx context.Context) { shop.RunPayments(ctx, q) }) // crashes and restarts
s.Run(20 * time.Second); s.Settle(60 * time.Second)      // faults, then quiet
```

The app is written against seams: `qenv.DB` and `qenv.Txn`, its own
broker interfaces, `qenv.Go`, `qenv.Now` and `qenv.Sleep`. Production
puts real clients behind them. Steps are recorded where effects happen:
`Txn.OnCommit`, a fake API server's interceptor, the provider. Then:

```go
sys := qtest.System{Binding: "checkout.binding.yaml", Run: func(c qenv.Chooser) error {
	_, err := checkout.Run(c, checkout.Config{Isolation: qenv.SnapshotIsolation, Mutation: shop.LockWithoutWrite})
	return err
}}
skew := qtest.AnyOf(qtest.Rejects("ship"), qtest.Rejects("cancel"))
sys.Explore(t, qtest.ExploreConfig{Seeds: 100}).Expect(t, skew)     // seeds, checked in one batch
quinthegel.Shrink(t, sys, hegel.WithTestCases(50)).Expect(t, skew)  // Hegel, shrunk to its interleaving
```

A `qtest.System` binds an application to its model once; `Explore`,
`Sweep` (covering arrays), `FollowTraces` (a model's traces' faults) and
`quinthegel.Shrink` all run it.

Seeds run in parallel, each recording into its own recorder. The
recorder travels in the chooser's context (`qenv.WithContext`) to every
task of the simulation. A step recorded without it, for example with
`context.Background()`, still lands in its run: qenv knows which
simulation each of its goroutines belongs to (its tasks, and the goroutine
that created it). During such runs recording is strict (`qobs.Strict`): a
step with nowhere to go panics, naming the fix, rather than vanish. That
happens only for a goroutine started with `go`, which `quintgo lint -sim
DIR` flags, along with wall-clock time. A Hegel test records the same way,
with `rec.Env(quinthegel.Chooser(tc))`, which also records the run's
choices: `qenv.Replay(v.Choices)` repeats a shrunk failure outside Hegel.

Code you cannot rewrite against seams (plain `sync.Mutex`,
`sync.RWMutex`, `sync.WaitGroup`, `go` statements) can be woven in at
build time. Import `qenv/weave` in the project's `orchestrion.tool.go`
and build with [qweave](qweave) (`qweave go test ./...`), this
repository's weaver, or with `orchestrion go test`. The root module's lock
and wait group calls then become simulated ones inside a simulation and
the real ones outside it, and a `go` statement marked `//qenv:go` starts a
simulation task. qweave reads the same aspect files as Orchestrion, with
types. It also weaves what Orchestrion misses: method values
(`return mu.RUnlock`) and calls promoted through an embedded lock.
`quintgo lint -weave DIR` lists what Orchestrion would miss.
[examples/edgepublish](examples/edgepublish) runs its publisher this way.

A whole program, standard library and network included, can run in
[gosim](https://github.com/jellevandenhooff/gosim) with its choices made by
a qenv `Chooser` from the test process (Seeded, Hegel, Replay):
[qgosim](qgosim), its own module on a patched gosim. A qenv deployment
moves there unchanged. `qenv.NewNative` runs the same doubles and
processes on plain goroutines and `time`, which gosim simulates, with
processes as gosim machines. Give your run function a `*qenv.Sim`, and
call it with `qenv.New(c)` or `qgosim.New()`.
gosim's translator also weaves Orchestrion aspects. List qobs in a
`gosim.tool.go`, and `//quint:action` records steps with no Orchestrion
build.
[examples/gosim](examples/gosim) has three systems on gosim: the Glomers
broadcast node over TCP on three machines, the checkout saga, and the
edgepublish publisher, unwoven by qenv/weave, each with its model checks
(DESIGN.md §14, §15.1, §15.8).

Each seeded run is checked in one quint batch, and the failing seed's
faults and step log are reported. `quinthegel.FindViolation` with
`quinthegel.Chooser` shrinks a failure to the interleaving and faults it needs.
See [examples/checkout](examples/checkout) (a saga at three isolation
levels) and [examples/fleetrollout](examples/fleetrollout) (a
multicluster-runtime controller, simulated and on real kwok clusters).

## 5. Metastable failures (`qload`, `qmeta`)

Some failures are not wrong answers but a state the system cannot leave.
A spike fills a queue, requests time out, and their retries keep the
queue full after the spike has passed. Isaacs et al. (HotOS '25) and
Alvaro et al. (2025) analyse these in tiers. quint-connect-go has each
tier:
- `qmeta`: a CTMC over (queue length, requests waiting to retry). It
  draws the field (`Chain.SVG`) and gives expected recovery times
  (`HittingTimes`) in seconds. `Calibrate` fits it to simulated
  trajectories.
- `qload`: a load simulation on qenv. Clients can be open-loop or closed
  (a controller polling its objects), with timeouts, retries, backoff and
  budgets, or the retry library under test itself (`ClientConfig.Retrier`,
  on virtual time). Servers have queues and workers, and every run yields
  a time series.
- `qload.Experiment`: the application itself under load. Customers
  (open-loop or a closed population) call it in a qenv simulation, and
  goodput is the rate of one of its own model steps. Probes sample
  backlogs, and the same steps can be checked against the model.
  `qmeta.Assess` judges a run's recovery: goodput back to the healthy
  rate, within the noise of the counts.
- `qrole` and `qmeta.Region.Rules`: the application marks its load roles
  (`//quint:serve`, `//quint:operation`, `//quint:call`), the chain is
  calibrated on their series, and the states it is unlikely to recover
  from in time become a Prometheus alerting rule over the same gauges,
  with the policy it assumes in its annotations. `qrole.MetricsSink`
  counts every model step by outcome.
- `quinthegel.FindTrigger` (qload systems) and `quinthegel.SearchTrigger` (any
  `qmeta.System`, such as `qmeta.Of(experiment)`): Hegel searches
  transient triggers (extra load, a slowdown, a duration) for one after
  which goodput does not recover, and shrinks it. Seeds of a trigger run
  in parallel.

```go
c := qmeta.New(qmeta.Params{Lambda: 9, Mu: 10, Timeout: 9, Retries: 3, Queue: 100})
h := c.HittingTimes(func(s qmeta.State) bool { return s.U <= 10 })   // seconds to drain, per state
r := quinthegel.FindTrigger(t, cfg, 9, qmeta.TriggerSpace{MaxExtraRate: 10}, 5*time.Minute)
// r.Trigger: "+1.0 req/s for 3m36s" tips immediate retries into a storm
```

The tiers check each other. In
[examples/retrystorm](examples/retrystorm), the simulation showed that
backoff alone makes recovery worse, which the first chain had missed.
Correctness stays with the model: the retry budget's accounting is
checked against Quint, which catches a bug that load tests hardly see.
[examples/crossdb](examples/crossdb) applies the tiers to a Crossplane
provider's requeue policy. [examples/checkout](examples/checkout) puts
the saga itself under load as a `qload.Experiment`, its payment provider
given a `qenv.Capacity` (workers, queue, service times, shedding):

```go
x := qload.Experiment{Rate: 3, Healthy: 3, Deploy: func(env *qload.Env) func(ctx context.Context, i int) {
	d := checkout.Deploy(env.Sim, cfg)   // cfg.Provider.Slowdown = env.Trigger.Factor()
	env.Probe("backlog", d.PayQueue.Pending)
	return func(ctx context.Context, i int) { d.Shop.PlaceOrder(ctx, fmt.Sprintf("o%d", i), "widget", 1) }
}}
r := qmeta.Assess(x, "markPaid", qmeta.Trigger{Slowdown: 3, Duration: time.Minute}, 5*time.Minute, seed)
```

## Packages

| Package | |
|---|---|
| `connect` | MBT driver runtime: `Run`, `Test`, `Replay`, `Switch`, `Pick[T]`, `StateDriver`, `GenerateRun`/`GenerateTest` ; `FaultScript` (a trace's faults imposed on a whole program in qenv) |
| `quinthegel` | Hegel integration: `Test` (modes `Batched`/`Each`/`After`/`Off`), `FindViolation` (expected failures, shrunk), `Recorder`, `CaseChecker`/`CheckCase`, `Replay`, `Traces`; the Hegel adapters of other packages: `Chooser` (qenv's choices from a test case), `FindTrigger`/`SearchTrigger` (qmeta's trigger search) ; `Shrink(t, sys)` (a `qtest.System` under Hegel, its failure replayable from `Violation.Choices`) |
| `itf` | ITF traces: `Parse`, `Value`, `Decode`/`Encode` (Go ⇄ Quint values), `ParseQuint` (Quint literals) |
| `qobs` | runtime recorder: `Record`, `Thread`, `WithRecorder` (a context's own recorder, so runs in parallel record apart), `SetFallback` (a goroutine's run, which qenv provides), `Strict` (a step with nowhere to go panics); sinks `MemorySink`, `FileSink`/`DurableFileSink`/`JSONLSink`, `OTelSink`, `Tee`; `orchestrion.yml` |
| `qtrace` | the step schema, `Reconstruct` (ordering, gaps, restarts), step log I/O |
| `otelio` | OTLP/JSON file exporter and reader; SDK spans to steps |
| `convert` | an application's own spans to steps, by YAML rules |
| `binding` | the YAML binding: lint against `quint typecheck`, annotation scan, plan, Quint module and ITF generation |
| `validate` | replay with `quint test`: `Checker.Check`, `CheckRuns`, `ValidateITF`, structured verdicts |
| `qtest` | record-then-check inside a Go test; `Expect` with `Conforms`/`Rejects`/`Mismatch`/`Violates`/`AnyOf`; `Explore` (seeded simulated runs, in parallel, checked in bulk); `Check`/`Validate` for steps recorded elsewhere; `Constants` (another instance of the binding's model); `RequireQuint` ; `SweepExplore` (a covering array of configurations, each row explored, expectations per row) ; claims over runs (`Some`, `All`, `None`: an expectation alone is about some run, `Conforms` about every run) ; `System` (an app bound to its model: `Explore`, `Sweep`, `FollowTraces`) ; `System.Fuzz` (Go's coverage-guided fuzzer, each input a run's choices, verdicts cached by steps: `VerdictCache`) ; `System.Mutate` (each mutant woven by `qweave -mutate` explored; survivors are code the model does not constrain: `MutationReport`) |
| `qenv` | deterministic simulated environments: scheduler, virtual time, crashable processes, choosers (seeded, replay, swarm; Hegel's is `quinthegel.Chooser`), a native runtime (`NewNative`: the same doubles on goroutines and `time`, for gosim), `Store` (isolation levels), `Queue`, `Service` (RPC, with optional `Capacity`: workers, queue, service times, shedding); `WithContext` (what every task's context carries, e.g. the run's recorder); crashes per virtual second (`NemesisEvery`); scheduler self-checks (`Check`, `QENV_CHECK=1`); `Lint` (what escapes a simulation: `go` statements, the wall clock) ; fault points (`FaultPoint`, `//quint:fault` via `qenv/fault`), fault coverage (`CoverageOf`, `qtest.Exploration.Coverage`), `Script` (exactly the faults it is given) ; `Without` (fault kinds off for a run); pair and order coverage of faults (`Coverage.Pairs`, `Untried`, `OneWay`) ; `Bytes`/`FromBytes` (choices from a byte stream, the rule Hegel's chooser and fuzzing share) and `EncodeChoices` (a recorded run as bytes: a fuzz corpus entry) ; fault kinds declared by name (`Faults`, `Sim.FaultOf`, `Only`), so coverage and scripts name options |
| `qenv/weave` | Orchestrion aspects and the functions they call: `sync` locks and wait groups, and `//qenv:go` goroutines, simulated inside a qenv simulation and real outside |
| `qenv/fault` | the aspect of `//quint:fault`: a function's entry becomes a fault choice (its package's own errors, or a crash) in a simulation, declared for coverage; woven by qweave |
| `qload` | load on qenv: `Experiment` (an application under open- or closed-loop load, goodput from its model steps, probes, seeded streams, `Trigger`s); `Simulate` (an abstract server and clients with timeouts, retries, backoff, budgets or a `Retrier` under test) ; `Piecewise` (a measured shape, e.g. a production trace's) |
| `qrole` | load roles: `//quint:serve`, `//quint:operation`, `//quint:call` (woven by its aspects, or `Serve`/`Operation`/`Call`) measure a server's requests (u) and the orbit (v); OTel gauges (`Instrument`) ; pprof labels per role (`Labels(true)`: profiles split by server and service) |
| `qmeta` | metastability: a CTMC (`Field`/`SVG`, `HittingTimes`, `RecoveryWithin`, `Expect`/`ExpectAll`, `Calibrate`/`CalibrateTimeout` from observed series via `Trajectories`), alerts (`Region`: states unlikely to recover in time; `Rules`: a Prometheus rule group over qrole's metrics), the recovery property (`Recovers`, `Assess`, `Mostly`; judged against the healthy goodput within the counts' noise), and `System`/`TriggerSpace` for Hegel's trigger search (`quinthegel.FindTrigger`, `SearchTrigger`) ; measured service times (`Measure`: μ, the coefficient of variation, `Exponential`, `Draw` for simulations) |
| `qcover` | code coverage by model step: blocks woven by `qweave -cover`, attributed to each task's next step; `Summarize` (never run, run by no step, outcomes that ran the same code); `ExploreConfig.Coverage`, `Exploration.CodeCoverage` |
| `qmut` | mutant schemata: `qweave -mutate` weaves negated conditions, moved comparison boundaries and dropped calls into one build, each behind a switch; a run turns one on (`WithMutant`) and notes which it reached (`Reach`) |
| `qrun` | a run's state (`Key[T]`, `Get`): from the context, else the goroutine's run; what qobs, qrole and qcover look up |
| `sktrace` | SimKube traces (format v3, no dependencies): a real cluster's objects over time (`Events`, `Lifecycles`), as rates, gauges (`Gauge`, `Replicas`) and pod starts, for `qload.Piecewise` load shapes and replayed workloads |
| `qsweep` | covering arrays: `Cover` (every t-way combination of factor values, with constraints), `Missing`, `Sequences` (every order of t events) |
| `load` | read OTLP/JSON, a step log or ITF, detected by content |
| `skills` | the agent skills below, embedded; install targets |
| `cmd/quintgo` | CLI: `scaffold`, `lint` (`-sim DIR`: simulation code; `-weave DIR`: woven code), `steps`, `validate`, `itf`, `convert`, `skills` |

## Example apps

Each one is a runnable Go app with a Quint model, a binding, mutants that
inject real bugs, a `run-demo.sh` and tests showing which check catches
which bug.

| App | What it is | What it shows |
|---|---|---|
| [`examples/checkout`](examples/checkout) | a checkout saga: orders, payments with idempotency keys and voids, shipping, cancellation, a transactional outbox | fault injection with `qenv`: crashable services, a database at three isolation levels, an at-least-once broker, a lossy network; which bug is a bug at which isolation level (lost update, write skew); a double charge; a lost event; Hegel shrinking a write skew; under load, too many payment consumers collapse on their own, a retry budget is bypassed by broker redelivery, deadline propagation recovers, and idempotency keys hold through the storm |
| [`examples/fleetrollout`](examples/fleetrollout) | a multicluster-runtime controller rolling a Deployment across member clusters within a budget (own module) | the same controller simulated (fake clients behind interceptors: status lag, lost responses, crashes) and on three real kwok clusters (`kill -9` included), checked by one evidence-based model; `observedGeneration` and in-memory-state bugs caught in both |
| [`examples/crossdb`](examples/crossdb) | a Crossplane provider for a database-as-a-service (crossplane-runtime's managed reconciler), with connection details written back as Secrets (own module) | Crossplane's real reconcile logic simulated under lost responses, crashes, throttling and an eventually consistent cloud, then the same provider on kind with full Crossplane composing `AppDatabase`s; why the external name must be deterministic and the create idempotent; a leak with no crash; translating a library's real-clock timestamps at the seam; the provider's requeue policy against a capacity dip in the cloud (`qload`, closed loop) |
| [`examples/retrystorm`](examples/retrystorm) | a client retry library: bounded retries, backoff with jitter, a retry budget | metastable failures in tiers: a CTMC predicts which policies recover from a spike, a simulation (the library on virtual time) confirms it, and Hegel shrinks the smallest trigger; backoff alone made things worse; alerting rules from the chain calibrated on the app's own load roles; the budget's accounting checked against a Quint model, which catches a bug load tests barely see |
| [`examples/glomers`](examples/glomers) | Fly.io's Gossip Glomers: unique ids, broadcast, grow-only counter, kafka-style log, on a Maelstrom runtime and simulator | distributed systems: models of each challenge with their properties; Lamport clocks to merge per-node step logs; Hegel scheduling deliveries, losses and partitions, and shrinking a lost-update bug to three steps; the same nodes under real Maelstrom, their logs conforming to the models |
| [`examples/kvstore`](examples/kvstore) | an HTTP key-value service with compare-and-swap (`kvserver`, `kvload`) | a concurrent service checked as a serial behaviour of its model, from its steps or its plain OTel spans (`kv.op` as a linearization key); a CAS race caught under load; errors that classify themselves; reads checked against the model; Hegel over HTTP |
| [`examples/jobqueue`](examples/jobqueue) | an at-least-once job queue with leases, workers and restarts (`jobqueue`) | all three verdicts: an implementation bug, a design property the run violates (`exactlyOnce`, checked with `-invariants`), and conformance; restarts inferred from incarnations; when model-driven testing does not fit |
| [`examples/bank`](examples/bank) | an in-memory bank (library) | every workflow on one small system: what model-driven testing cannot catch and Hegel + the model can |
| [`examples/edgepublish`](examples/edgepublish) | the spike's real case: an OTel exporter's publish protocol with S3 faults and crashes (own module, Orchestrion) | `//quint:action` annotations; three trace paths with identical verdicts; MBT with gated effects; model → Go → telemetry → model; the publisher, unchanged, in a qenv simulation by weaving its locks and goroutines (`qenv/weave`), where exploration found a race the real exporter had already fixed |
| [`examples/gosim`](examples/gosim) | the Glomers broadcast node over real TCP on three gosim machines, and the checkout saga's deployment on qenv's native runtime, both unchanged (own module, `qgosim`) | whole-program simulation whose every choice (schedule, `select`, map order, faults) comes from a qenv `Chooser` in the test process; runs checked against the same models and bindings; exact replay; `qtest.Explore` over parallel gosim binaries; Hegel shrinking fire-and-forget gossip's lost value; a mutant's second symptom that gosim's schedules reach |

```sh
examples/glomers/maelstrom.sh                   # under real Maelstrom, then quintgo on the nodes' step logs
examples/kvstore/run-demo.sh                    # MUTATION=stale-cas | revision-on-conflict
examples/jobqueue/run-demo.sh                   # MUTATION=ack-any-lease | forget-done
examples/checkout/run-demo.sh                   # MUTATION=no-locks | lock-without-write | no-idempotency-key | dual-write
examples/fleetrollout/kwok-demo.sh              # MUTATION=ignore-observed-generation | in-memory-progress, KILL=1
examples/crossdb/kind/demo.sh                   # kind + Crossplane; MUTATION=no-client-token | generated-external-name | password-from-create, DROP=0.3, KILL=1
```

## Agent skills and guidance

The skills in [`skills/`](skills) teach a coding agent this toolkit:
- `quint-model-for-go`: model conventions;
- `quint-mbt-driver`: drivers;
- `quint-trace-validation`: instrumentation, bindings and verdicts;
- `quint-hegel`: Hegel + Quint;
- `quint-modeling-patterns`: what to put in the model: the requirement
  rather than one solution, the environment's real guarantees,
  linearization points, ghost variables, settle-conditioned properties,
  mechanism models with a lemma;
- `quint-verification-workflow`: the order of work (model, simulate,
  record, bind, mutants, Hegel, real environment) and triage of verdicts;
- `quint-fault-injection`: seams, `qenv` deployments, contract profiles,
  `qtest.Explore`, shrinking with `quinthegel.Chooser`, and the real environment;
- `quint-metastability`: retry storms and other metastable failures: the
  CTMC, the load simulation, trigger search, mitigations.

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
