#!/usr/bin/env bash
# Run the kvstore service under concurrent load, then check what it recorded
# against model/kv.qnt two ways: its model steps, and its plain OTel spans.
#   OUT=dir       where binaries and recordings go (default: a temp dir)
#   MUTATION=...  stale-cas | revision-on-conflict (default: none)
set -uo pipefail
cd "$(dirname "$0")"
OUT=${OUT:-$(mktemp -d)}
mkdir -p "$OUT"
echo "# output in $OUT"
(cd ../.. && go build -o "$OUT/quintgo" ./cmd/quintgo) || exit 2
go build -o "$OUT/kvserver" ./cmd/kvserver || exit 2
go build -o "$OUT/kvload" ./cmd/kvload || exit 2

"$OUT/kvserver" -addr 127.0.0.1:18080 -steps "$OUT/steps.jsonl" -spans "$OUT/spans.jsonl" -mutation "${MUTATION:-}" &
srv=$!
for _ in $(seq 50); do curl -s -o /dev/null 127.0.0.1:18080/kv/a && break; sleep 0.1; done
"$OUT/kvload" -addr 127.0.0.1:18080 -clients 8 -ops 50 -keys a,b || { kill $srv; exit 2; }
kill -INT $srv; wait $srv

echo; echo "\$ quintgo validate -binding kv.binding.yaml steps.jsonl"
"$OUT/quintgo" validate -binding kv.binding.yaml -dir "$OUT/v-steps" "$OUT/steps.jsonl" | grep -E 'input|conformance|invariant|step \[|quint:|expect:|observed:|obs:|RESULT'
echo; echo "\$ quintgo validate -binding kv.binding.yaml -convert kv.convert.yaml spans.jsonl"
"$OUT/quintgo" validate -binding kv.binding.yaml -convert kv.convert.yaml -dir "$OUT/v-spans" "$OUT/spans.jsonl" | grep -E 'input|conformance|invariant|step \[|quint:|expect:|observed:|obs:|RESULT'
