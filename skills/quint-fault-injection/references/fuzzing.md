# bis Fuzzing: Go's fuzzer drives the simulation

```go
func FuzzShop(f *testing.F) { sys.Fuzz(f, rates) }   // sys is a qtest.System
```

- Each fuzz input is one run: its bytes are the run's choices
  (`qenv.FromBytes`, the same byte rule as Hegel's chooser), and the model
  judges it. `go test` runs the corpus as regression tests; `go test
  -fuzz FuzzShop -fuzztime 60s` explores, guided by coverage of the code
  under test.
- Verdicts are cached by the run's steps (for the test binary): most
  inputs repeat a run's model-level steps. On checkout, that took the
  fuzzer from 16 runs in 90 s to 14,633.
- Keep a shrunk Hegel failure, once fixed, as a seed:
  `sys.Fuzz(f, rates, must(qenv.EncodeChoices(v.Choices, rates)))`.
