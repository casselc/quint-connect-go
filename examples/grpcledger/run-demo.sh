#!/usr/bin/env bash
# Serve the ledger for real over gRPC with injected network faults, export
# the server's otelgrpc spans, and check them against the model with
# quintgo (converted by ledger.convert.yaml). MUTATION=no-dedupe |
# no-funds-check | split-txn injects a bug; SEEDS picks how many loads.
# The simulated tier (every fault, crash and interleaving a qenv choice) is
# in the tests: go test ./... (DESIGN: examples/grpcledger/README.md).
set -euo pipefail
cd "$(dirname "$0")"
out=$(mktemp -d)
go build -o "$out/ledger-real" ./cmd/ledger-real
go build -C ../.. -o "$out/quintgo" ./cmd/quintgo
status=0
for seed in $(seq 1 "${SEEDS:-3}"); do
  echo "== load $seed ${MUTATION:+(mutation $MUTATION)}"
  "$out/ledger-real" -seed "$seed" -mutation "${MUTATION:-}" -out "$out/spans$seed.json"
  if "$out/quintgo" validate -noitf -binding ledger.binding.yaml -convert ledger.convert.yaml "$out/spans$seed.json" > "$out/report$seed"; then
    echo "conforms"
  else
    status=1
    grep -E "^actor|FAIL|not a transition|does not match|quint:|observed:" "$out/report$seed" | head -12
  fi
done
echo "spans and reports in $out"
exit $status
