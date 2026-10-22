#!/usr/bin/env bash
# Run the pipeline on each backend (simulated with faults, in-process
# Kafka, fake Kinesis), then check every run's step log against the model
# with quintgo. MUTATION=commit-first | no-dedupe | non-idempotent-producer
# | off-by-one injects a bug; BACKENDS picks backends; RUNS the runs each.
set -euo pipefail
cd "$(dirname "$0")"
out=$(mktemp -d)
go build -o "$out/streams-demo" ./cmd/streams-demo
go build -C ../.. -o "$out/quintgo" ./cmd/quintgo
status=0
for backend in ${BACKENDS:-sim kafka kinesis}; do
  runs=${RUNS:-20}
  [ "$backend" = sim ] || runs=${RUNS:-2}
  echo "== $backend ${MUTATION:+(mutation $MUTATION)}"
  "$out/streams-demo" -backend "$backend" -runs "$runs" -mutation "${MUTATION:-}" -out "$out/$backend"
  # one actor per run: the report lists each run's verdict
  if "$out/quintgo" validate -noitf -binding streams.binding.yaml "$out/$backend"/*.jsonl > "$out/$backend.report"; then
    echo "all runs conform"
  else
    status=1
    awk '/^actor/{if(b~/FAIL/)print b; b=$0; next}{b=b"\n"$0}END{if(b~/FAIL/)print b}' "$out/$backend.report" |
      grep -E "^actor|FAIL|not a transition|does not match|quint:" | head -24
  fi
done
echo "step logs and reports in $out"
exit $status
