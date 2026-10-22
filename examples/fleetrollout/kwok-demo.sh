#!/usr/bin/env bash
# Roll web:v2 out across three kwok clusters with the real controller
# (multicluster-runtime), then check its step logs against the model.
#
#   MUTATION=ignore-observed-generation | in-memory-progress   inject a bug
#   KILL=1     kill -9 the controller after its first patch, then start it again
#   KEEP=1     leave the clusters running (they are reused next time)
#   TRACE=f    time pods by a SimKube trace's lifetimes (f relative to this
#              directory; cmd/kwok-stages writes kwok Stages, SPEED times
#              faster, default 10), on clusters of their own
#
# Needs kwokctl and kwok on PATH (go install sigs.k8s.io/kwok/cmd/{kwokctl,kwok}@v0.7.0);
# the binary runtime downloads etcd and the Kubernetes binaries, no Docker.
set -euo pipefail
cd "$(dirname "$0")"
members=(m1 m2 m3)
out=$(mktemp -d)
go build -o "$out/fleet-controller" ./cmd/fleet-controller
(cd ../.. && go build -o "$out/quintgo" ./cmd/quintgo)

# Clusters timed by a trace are named apart, so KEEP's reuse never mixes
# them with kwok's default (immediate) pod stages.
prefix=fleet
create=()
if [[ -n "${TRACE:-}" ]]; then
  go run ./cmd/kwok-stages -trace "$TRACE" -speed "${SPEED:-10}" >"$out/stages.yaml"
  prefix=fleet-trace
  create=(--config "$out/stages.yaml")
  echo "pods timed by $TRACE ($(grep -c 'name: pod-ready-' "$out/stages.yaml") readiness options, ${SPEED:-10}x)"
fi
args=()
for m in "${members[@]}"; do
  c="$prefix-$m"
  kwokctl get clusters 2>/dev/null | grep -qx "$c" || kwokctl create cluster --name "$c" --runtime binary --wait 2m "${create[@]}" >/dev/null 2>&1
  k="kwokctl --name $c kubectl"
  $k apply -f kwok/fleet.yaml >/dev/null
  $k set image deploy/web web=web:v1 >/dev/null # reset a previous run
  $k rollout status deploy/web --timeout 180s >/dev/null
  if [[ -n "${TRACE:-}" ]] && ! grep -q 'name: pod-ready-0' "$HOME/.kwok/clusters/$c/kwok.yaml"; then
    echo "cluster $c is not running the trace's stages ($HOME/.kwok/clusters/$c/kwok.yaml)"; exit 1
  fi
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
  kwokctl --name "$prefix-$m" kubectl rollout status deploy/web --timeout 180s >/dev/null
done
until [[ "$(for m in "${members[@]}"; do kwokctl --name "$prefix-$m" kubectl get deploy web -o jsonpath='{.spec.template.spec.containers[0].image}'; echo; done | sort -u)" == "web:v2" ]]; do sleep 0.5; done
for m in "${members[@]}"; do kwokctl --name "$prefix-$m" kubectl rollout status deploy/web --timeout 180s >/dev/null; done
kill "$pid"; wait "$pid" 2>/dev/null || true
"$out/fleet-controller" "${args[@]}" -verify -steps "$out/steps-verify.jsonl"

status=0
"$out/quintgo" validate -noitf -binding fleet.binding.yaml "$out"/steps-*.jsonl || status=$?
echo "step logs in $out"
if [[ -z "${KEEP:-}" ]]; then
  for m in "${members[@]}"; do kwokctl delete cluster --name "$prefix-$m" >/dev/null 2>&1 || true; done
fi
exit $status
