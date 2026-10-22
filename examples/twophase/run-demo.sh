#!/usr/bin/env bash
# Simulate two-phase commit, then check the step logs against the Choreo
# model with quintgo. MUTATION=early-commit | impatient | volatile-decision
# injects a bug.
set -euo pipefail
cd "$(dirname "$0")"
out=$(mktemp -d)
go build -o "$out/twophase-sim" ./cmd/twophase-sim
go build -o "$out/quintgo" ../../cmd/quintgo
echo "== two-phase commit ${MUTATION:+(mutation $MUTATION)}"
"$out/twophase-sim" -seeds "${SEEDS:-30}" -mutation "${MUTATION:-}" -out "$out/runs"
if "$out/quintgo" validate -noitf -binding twophase.binding.yaml "$out/runs"/*.jsonl > "$out/report"; then
  echo "all runs conform"
else
  awk '/^actor/{if(b~/FAIL/)print b; b=$0; next}{b=b"\n"$0}END{if(b~/FAIL/)print b}' "$out/report" |
    grep -E "^actor|FAIL|not a transition|does not match|quint:" | head -24
  echo "step logs and report in $out"
  exit 1
fi
echo "step logs and report in $out"
