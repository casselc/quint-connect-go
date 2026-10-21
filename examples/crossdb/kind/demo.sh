#!/usr/bin/env bash
# The provider against a real cluster with Crossplane (kind/setup.sh): the
# driver creates AppDatabases (composed into Databases), deletes some; the
# provider makes the dbaas server follow; the server's and the driver's
# step logs are checked against the model.
#
#   MUTATION=generated-external-name | no-client-token | password-from-create
#   DROP=0.2    probability the dbaas server drops a mutating response (after applying it)
#   KILL=1      kill -9 the provider every 2-6 s while the driver runs, and restart it
#   N=4 SEED=1  AppDatabases, and the driver's seed
#
# Exit status: 0 conforms, 1 violation (quintgo's).
set -euo pipefail
cd "$(dirname "$0")/.."
kind/setup.sh
cluster=${CLUSTER:-crossdb}
out=$(mktemp -d)
for c in dbaas-server provider driver; do go build -o "$out/$c" "./cmd/$c"; done
(cd ../.. && go build -o "$out/quintgo" ./cmd/quintgo)
kind get kubeconfig --name "$cluster" > "$out/kubeconfig"
export KUBECONFIG="$out/kubeconfig"
"$out/driver" -cleanup

"$out/dbaas-server" -listen 127.0.0.1:8085 -steps "$out/cloud.jsonl" -drop-response "${DROP:-0.1}" \
  -visibility-lag 2s -seed "${SEED:-1}" 2>"$out/server.log" &
server=$!
run_provider() { "$out/provider" -dbaas http://127.0.0.1:8085 -poll 2s -mutation "${MUTATION:-}" >>"$out/provider.log" 2>&1 & echo $!; }
provider=$(run_provider)
killer=""
if [[ -n "${KILL:-}" ]]; then
  (
    while true; do
      sleep $((2 + RANDOM % 5))
      pkill -9 -x provider || true
      echo "killed the provider" >&2
      run_provider >/dev/null
    done
  ) &
  killer=$!
fi

status=0
"$out/driver" -n "${N:-4}" -seed "${SEED:-1}" -steps "$out/driver.jsonl" -wait "${WAIT:-3m}" || status=$?
[[ -n "$killer" ]] && kill "$killer" 2>/dev/null
pkill -x provider || true
kill "$server" 2>/dev/null || true
wait 2>/dev/null || true
[[ $status -eq 0 ]] || { echo "driver failed"; exit 2; }

"$out/quintgo" validate -noitf -binding crossdb.binding.yaml "$out/cloud.jsonl" "$out/driver.jsonl" || status=$?
echo "step logs and logs in $out"
exit $status
