#!/usr/bin/env bash
# Run the job queue with workers, slow jobs and a restart; check the run.
#   OUT=dir       where binaries and the step log go (default: a temp dir)
#   MUTATION=...  ack-any-lease | forget-done (default: none)
set -uo pipefail
cd "$(dirname "$0")"
OUT=${OUT:-$(mktemp -d)}
mkdir -p "$OUT"
echo "# output in $OUT"
(cd ../.. && go build -o "$OUT/quintgo" ./cmd/quintgo) || exit 2
go run ./cmd/jobqueue -steps "$OUT/steps.jsonl" -mutation "${MUTATION:-}" || exit 2
show() { grep -E 'issue|conformance|invariant|step \[|quint:|observed:|RESULT|violated'; }
echo; echo "\$ quintgo validate -binding jobqueue.binding.yaml steps.jsonl"
"$OUT/quintgo" validate -binding jobqueue.binding.yaml -dir "$OUT/v" "$OUT/steps.jsonl" | show
echo; echo "\$ quintgo validate -binding jobqueue.binding.yaml -invariants exactlyOnce steps.jsonl"
"$OUT/quintgo" validate -binding jobqueue.binding.yaml -invariants exactlyOnce -dir "$OUT/v1" "$OUT/steps.jsonl" | show
