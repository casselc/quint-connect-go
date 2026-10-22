#!/usr/bin/env bash
# The retry storm report, and a check of the retry budget's recorded steps.
#   OUT=dir       where the report, binaries and the step log go (default: a temp dir)
#   LAMBDA=9      base load (requests/s)
#   MUTATION=...  uncapped | deposit-on-retry (default: none)
set -uo pipefail
cd "$(dirname "$0")"
OUT=${OUT:-$(mktemp -d)}
mkdir -p "$OUT"
echo "# output in $OUT"
(cd ../.. && go build -o "$OUT/quintgo" ./cmd/quintgo) || exit 2
go run ./cmd/retrystorm-report -out "$OUT/report" -lambda "${LAMBDA:-9}" -steps "$OUT/steps.jsonl" -mutation "${MUTATION:-}" || exit 2
echo; echo "\$ quintgo validate -binding retrystorm.binding.yaml steps.jsonl"
"$OUT/quintgo" validate -binding retrystorm.binding.yaml -dir "$OUT/v" "$OUT/steps.jsonl" > "$OUT/verdict.txt"; rc=$?
grep -E 'issue|conformance|invariant|step [0-9]+ |quint:|observed:|RESULT|violated' "$OUT/verdict.txt"
exit $rc
