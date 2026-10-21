#!/usr/bin/env bash
# Simulate the shop under each isolation level, then check the step logs
# against the model with quintgo. MUTATION=no-locks | lock-without-write |
# no-idempotency-key | dual-write injects a bug; ISOLATIONS picks levels.
set -euo pipefail
cd "$(dirname "$0")"
out=$(mktemp -d)
go build -o "$out/checkout-sim" ./cmd/checkout-sim
go build -o "$out/quintgo" ../../cmd/quintgo
status=0
for iso in ${ISOLATIONS:-serializable snapshot read-committed}; do
  echo "== $iso ${MUTATION:+(mutation $MUTATION)}"
  "$out/checkout-sim" -seeds "${SEEDS:-30}" -isolation "$iso" -mutation "${MUTATION:-}" -crash-rate "${CRASH_RATE:-0.05}" -out "$out/$iso"
  # one actor per seed: the report lists each run's verdict
  if "$out/quintgo" validate -noitf -binding checkout.binding.yaml "$out/$iso"/*.jsonl > "$out/$iso.report"; then
    echo "all runs conform"
  else
    status=1
    # the failing runs' verdicts
    awk '/^actor/{if(b~/FAIL/)print b; b=$0; next}{b=b"\n"$0}END{if(b~/FAIL/)print b}' "$out/$iso.report" |
      grep -E "^actor|FAIL|not a transition|does not match|quint:" | head -24
  fi
done
echo "step logs and reports in $out"
exit $status
