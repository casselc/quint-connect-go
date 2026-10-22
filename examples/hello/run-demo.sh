#!/usr/bin/env bash
# Simulate the counter and its retrying client, then check the step logs
# against the model with quintgo. MUTATION=no-dedupe injects the bug.
set -euo pipefail
cd "$(dirname "$0")"
out=$(mktemp -d)
go build -o "$out/hello-sim" ./cmd/hello-sim
go build -o "$out/quintgo" ../../cmd/quintgo
echo "== hello ${MUTATION:+(mutation $MUTATION)}"
"$out/hello-sim" -seeds "${SEEDS:-20}" -mutation "${MUTATION:-}" -out "$out/runs"
if "$out/quintgo" validate -noitf -binding hello.binding.yaml "$out/runs"/*.jsonl > "$out/report"; then
  echo "all runs conform"
else
  # the first failing run: what was observed, and the state the model was in
  awk '/^actor/{if(b~/FAIL/){print b; done=1; exit} b=$0; next}{b=b"\n"$0}END{if(!done && b~/FAIL/)print b}' "$out/report" |
    grep -vE "module:|N/A"
  echo "step logs and report in $out"
  exit 1
fi
echo "step logs and report in $out"
