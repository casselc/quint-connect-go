#!/usr/bin/env bash
# Roll web:v2 out across three kwok clusters with the real controller
# (multicluster-runtime), then check its step logs against the model.
#
#   MUTATION=ignore-observed-generation | in-memory-progress   inject a bug
#   KILL=1     kill -9 the controller after its first patch, then start it again
#   KEEP=1     leave the clusters running (they are reused next time)
#
# Needs kwokctl and kwok on PATH (go install sigs.k8s.io/kwok/cmd/{kwokctl,kwok}@v0.7.0);
# the binary runtime downloads etcd and the Kubernetes binaries, no Docker.
set -euo pipefail
cd "$(dirname "$0")"
members=(m1 m2 m3)
out=$(mktemp -d)
go build -o "$out/fleet-controller" ./cmd/fleet-controller
(cd ../.. && go build -o "$out/quintgo" ./cmd/quintgo)

args=()
for m in "${members[@]}"; do
  c="fleet-$m"
  kwokctl get clusters 2>/dev/null | grep -qx "$c" || kwokctl create cluster --name "$c" --runtime binary --wait 2m >/dev/null 2>&1
  k="kwokctl --name $c kubectl"
  $k apply -f kwok/fleet.yaml >/dev/null
  $k set image deploy/web web=web:v1 >/dev/null # reset a previous run
  $k rollout status deploy/web --timeout 180s >/dev/null
  args+=(-member "$m=$HOME/.kwok/clusters/$c/kubeconfig.yaml")
done
echo "fleet ready at web:v1: ${members[*]}"

start() { "$out/fleet-controller" "${args[@]}" -mutation "${MUTATION:-}" -steps "$out/steps-$1.jsonl" >"$out/controller-$1.log" 2>&1 & pid=$!; }
start 1
if [[ -n "${KILL:-}" ]]; then
  until grep -q '"patch"' "$out/steps-1.jsonl" 2>/dev/null; do sleep 0.1; done
  sleep 0.3
  kill -9 "$pid"; wait "$pid" 2>/dev/null || true
  echo "killed the controller after its first patch; starting it again"
  start 2
fi
for m in "${members[@]}"; do
  kwokctl --name "fleet-$m" kubectl rollout status deploy/web --timeout 180s >/dev/null
done
until [[ "$(for m in "${members[@]}"; do kwokctl --name "fleet-$m" kubectl get deploy web -o jsonpath='{.spec.template.spec.containers[0].image}'; echo; done | sort -u)" == "web:v2" ]]; do sleep 0.5; done
for m in "${members[@]}"; do kwokctl --name "fleet-$m" kubectl rollout status deploy/web --timeout 180s >/dev/null; done
kill "$pid"; wait "$pid" 2>/dev/null || true
"$out/fleet-controller" "${args[@]}" -verify -steps "$out/steps-verify.jsonl"

status=0
"$out/quintgo" validate -noitf -binding fleet.binding.yaml "$out"/steps-*.jsonl || status=$?
echo "step logs in $out"
if [[ -z "${KEEP:-}" ]]; then
  for m in "${members[@]}"; do kwokctl delete cluster --name "fleet-$m" >/dev/null 2>&1 || true; done
fi
exit $status
