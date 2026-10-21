#!/usr/bin/env bash
# Create (or reuse) a kind cluster with Crossplane, function-patch-and-
# transform, the provider's CRD and the platform API (kind/platform.yaml).
#
#   CLUSTER=crossdb       the kind cluster's name
#   CA_BUNDLE=/path.pem   a CA to trust for image and package pulls (a TLS-
#                         intercepting proxy); also given to Crossplane
#
# Two host quirks are handled, both needed in sandboxed VMs:
#   - containerd's restrict_oom_score_adj: a host that forbids lowering
#     oom_score_adj makes every host-network pod (the control plane's)
#     fail with runc's "can't get final child's PID from pipe: EOF";
#   - kind copies the host's HTTP(S)_PROXY into the node, where a proxy on
#     127.0.0.1 is unreachable: the cluster is created without them.
set -euo pipefail
cd "$(dirname "$0")/.."
cluster=${CLUSTER:-crossdb}
crossplane=2.4.2

if ! kind get clusters 2>/dev/null | grep -qx "$cluster"; then
  cfg=$(mktemp)
  cat > "$cfg" <<'EOF'
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
containerdConfigPatches:
- |-
  [plugins."io.containerd.grpc.v1.cri"]
    restrict_oom_score_adj = true
EOF
  env -u HTTPS_PROXY -u https_proxy -u HTTP_PROXY -u http_proxy -u NO_PROXY -u no_proxy \
    kind create cluster --name "$cluster" --image kindest/node:v1.34.0 --config "$cfg" --wait 180s
  if [[ -n "${CA_BUNDLE:-}" ]]; then
    for node in $(kind get nodes --name "$cluster"); do
      docker cp "$CA_BUNDLE" "$node:/usr/local/share/ca-certificates/extra-ca.crt"
      docker exec "$node" sh -c 'update-ca-certificates >/dev/null 2>&1 && systemctl restart containerd'
    done
  fi
fi
kubectl config use-context "kind-$cluster" >/dev/null

if ! helm status crossplane -n crossplane-system >/dev/null 2>&1; then
  helm repo add crossplane-stable https://charts.crossplane.io/stable >/dev/null
  helm repo update crossplane-stable >/dev/null
  kubectl create namespace crossplane-system --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  ca=()
  if [[ -n "${CA_BUNDLE:-}" ]]; then
    kubectl -n crossplane-system create configmap ca-bundle --from-file=ca-bundle.crt="$CA_BUNDLE" \
      --dry-run=client -o yaml | kubectl apply -f - >/dev/null
    ca=(--set registryCaBundleConfig.name=ca-bundle --set registryCaBundleConfig.key=ca-bundle.crt)
  fi
  helm install crossplane crossplane-stable/crossplane --version "$crossplane" -n crossplane-system \
    "${ca[@]}" --wait --timeout 6m >/dev/null
fi

kubectl apply -f - >/dev/null <<'EOF'
apiVersion: pkg.crossplane.io/v1
kind: Function
metadata:
  name: function-patch-and-transform
spec:
  package: xpkg.crossplane.io/crossplane-contrib/function-patch-and-transform:v0.9.0
EOF
kubectl wait function/function-patch-and-transform --for=condition=Healthy --timeout=300s >/dev/null
kubectl apply -f package/crds/ >/dev/null
kubectl apply -f kind/platform.yaml >/dev/null
kubectl wait xrd/appdatabases.platform.example.org --for=condition=Established --timeout=120s >/dev/null
echo "kind cluster $cluster ready: Crossplane $crossplane, the dbaas CRD, AppDatabase"
