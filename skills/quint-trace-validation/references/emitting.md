# Emitting steps: keys, outcomes, sinks

Keys in `Record` and annotations:
- `actor`, `process`, `thread`, `outcome` override the step's identity;
- `when:ok|error`: record only on success or only on failure;
- `obs.<name>`: an observation of the state after the step;
- `order.<domain>`: a logical clock;
- anything else is an argument.

The final `error` is the outcome. An error can classify itself with a
`QuintOutcome() string` method (e.g. `"ambiguous"`).

**Record at the moment the step takes effect**, while holding the lock that
orders it, and before spawning work that depends on it.

Sinks (`qobs.NewRecorder(sink)`, installed with `qobs.SetGlobal`):
- `MemorySink` for tests;
- `FileSink("steps.jsonl")` for a native step log;
- `NewOTelSink(tp)` for span events. Use a TracerProvider that never
  samples them out: a dropped step is a reported gap;
- `Tee{...}` to send steps to several sinks.

