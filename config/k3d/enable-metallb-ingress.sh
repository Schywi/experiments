#!/usr/bin/env bash

set -euo pipefail

command -v helm >/dev/null 2>&1 || { echo "helm is required" >&2; exit 1; }
command -v kubectl >/dev/null 2>&1 || { echo "kubectl is required" >&2; exit 1; }

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "${script_dir}/../.." && pwd)"
base_values="${repo_root}/config/helm/cilium/values.yaml"
metallb_values="${repo_root}/config/metallb/cilium-values.yaml"
hubble_service_file="${repo_root}/config/helm/cilium/hubble-ui-service.yaml"
cluster_name="${K3D_CLUSTER_NAME:-cilium-lab}"
server_container="k3d-${cluster_name}-server-0"
cilium_version="${CILIUM_VERSION:-1.20.1}"
helm_timeout="${HELM_TIMEOUT:-10m}"

server_ip="$(docker inspect --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "${server_container}" 2>/dev/null || true)"
[[ -n "${server_ip}" ]] || {
  echo "could not resolve the k3d server container IP" >&2
  exit 1
}

helm upgrade --install cilium cilium/cilium \
  --namespace kube-system \
  --version "${cilium_version}" \
  --values "${base_values}" \
  --values "${metallb_values}" \
  --set-string k8sServiceHost="${server_ip}" \
  --set-string k8sServicePort=6443 \
  --wait \
  --timeout "${helm_timeout}"

kubectl --namespace kube-system apply --filename "${hubble_service_file}"
kubectl --namespace kube-system rollout restart daemonset/cilium
kubectl --namespace kube-system rollout status daemonset/cilium --timeout="${helm_timeout}"
kubectl --namespace kube-system rollout status deployment/cilium-operator --timeout="${helm_timeout}"

echo "Cilium ingress is configured as a MetalLB LoadBalancer"
