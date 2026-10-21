#!/usr/bin/env bash
# Run the Gossip Glomers solutions under real Maelstrom (Jepsen), then check
# the step logs the nodes wrote against the Quint models.
#
#   ./maelstrom.sh [challenge...]      default: echo uniqueids broadcast gcounter kafka
#   MAELSTROM=/path/to/maelstrom       an existing install (default: download v0.2.4 into $OUT)
#   OUT=dir                            binaries, Maelstrom stores and step logs (default: a temp dir)
#   GLOMERS_MUTATION=n                 run a package's Mutation n instead of the faithful node
#
# Needs Java 11+, gnuplot and quint. Maelstrom checks the challenge's own
# properties; quintgo checks each node's model steps, merged by their
# Lamport clocks. (kafka's model steps include lin-kv's own operations,
# which only the in-process Sim records, so it is checked by Maelstrom only.)
set -uo pipefail
cd "$(dirname "$0")"
OUT=${OUT:-$(mktemp -d)}
mkdir -p "$OUT/bin"
if [ -z "${MAELSTROM:-}" ]; then
  if [ ! -x "$OUT/maelstrom/maelstrom" ]; then
    curl -fsSL https://github.com/jepsen-io/maelstrom/releases/download/v0.2.4/maelstrom.tar.bz2 | tar xj -C "$OUT" || exit 2
  fi
  MAELSTROM=$OUT/maelstrom/maelstrom
fi
(cd ../.. && go build -o "$OUT/bin/quintgo" ./cmd/quintgo) || exit 2
status=0
for c in ${@:-echo uniqueids broadcast gcounter kafka}; do
  go build -o "$OUT/bin/$c" "./cmd/$c" || exit 2
  steps="$OUT/steps-$c"; rm -rf "$steps"; mkdir -p "$steps"
  case $c in
    echo)      args=(-w echo --node-count 1 --time-limit 5) ;;
    uniqueids) args=(-w unique-ids --time-limit 10 --rate 50 --node-count 3 --availability total --nemesis partition) ;;
    broadcast) args=(-w broadcast --node-count 5 --time-limit 20 --rate 10 --nemesis partition) ;;
    gcounter)  args=(-w g-counter --node-count 3 --rate 50 --time-limit 15 --nemesis partition) ;;
    kafka)     args=(-w kafka --node-count 2 --concurrency 2n --time-limit 15 --rate 200) ;;
    *) echo "unknown challenge $c"; exit 2 ;;
  esac
  echo "=== $c: maelstrom test ${args[*]}"
  (cd "$OUT" && QUINT_STEPS_DIR="$steps" "$MAELSTROM" test --bin "$OUT/bin/$c" "${args[@]}" 2>&1 | tail -1) | tee "$OUT/$c.result"
  grep -q "Everything looks good" "$OUT/$c.result" || status=1
  [ "$c" = kafka ] && continue
  for b in "$c"/*.binding.yaml; do
    [ -f "$b" ] && ls "$steps"/*.jsonl >/dev/null 2>&1 || continue
    echo "--- quintgo validate -noitf -binding $b ($(cat "$steps"/*.jsonl | wc -l) steps)"
    v=$(basename "$b" .yaml)
    "$OUT/bin/quintgo" validate -noitf -binding "$b" -dir "$OUT/v-$v" "$steps"/*.jsonl > "$OUT/$v.validate" || status=1
    grep -E 'issue|conformance|invariant|step \[|quint:|RESULT' "$OUT/$v.validate"
  done
done
exit $status
