#!/usr/bin/env bash

set -euo pipefail

command -v helm >/dev/null 2>&1 || {
  echo "helm is required" >&2
  exit 1
}
command -v kubectl >/dev/null 2>&1 || {
  echo "kubectl is required" >&2
  exit 1
}
command -v docker >/dev/null 2>&1 || {
  echo "docker is required" >&2
  exit 1
}

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "${script_dir}/../.." && pwd)"
values_file="${repo_root}/config/helm/cilium/values.yaml"

[[ -f "${values_file}" ]] || {
  echo "Cilium values file not found: ${values_file}" >&2
  exit 1
}

CILIUM_VERSION="${CILIUM_VERSION:-1.16.5}"
HELM_TIMEOUT="${HELM_TIMEOUT:-10m}"
CLUSTER_NAME="${K3D_CLUSTER_NAME:-cilium-lab}"
SERVER_CONTAINER="k3d-${CLUSTER_NAME}-server-0"
K8S_SERVICE_HOST="${K8S_SERVICE_HOST:-$(docker inspect --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "${SERVER_CONTAINER}" 2>/dev/null || true)}"

if [[ -z "${K8S_SERVICE_HOST}" ]]; then
  echo "could not resolve the k3d server container IP; is ${CLUSTER_NAME} running?" >&2
  exit 1
fi

helm repo add cilium https://helm.cilium.io --force-update
helm repo update cilium

helm upgrade --install cilium cilium/cilium \
  --namespace kube-system \
  --version "${CILIUM_VERSION}" \
  --values "${values_file}" \
  --set-string k8sServiceHost="${K8S_SERVICE_HOST}" \
  --set k8sServicePort=6443 \
  --wait \
  --timeout "${HELM_TIMEOUT}"

echo "Cilium ${CILIUM_VERSION} installed in the k3d cluster"
