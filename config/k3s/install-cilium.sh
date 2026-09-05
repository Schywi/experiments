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

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "${script_dir}/../.." && pwd)"
values_file="${repo_root}/config/helm/cilium/values.yaml"

[[ -f "${values_file}" ]] || {
  echo "Cilium values file not found: ${values_file}" >&2
  exit 1
}

# 1.16.5 is a known chart/app pairing. Upgrade this pin deliberately alongside
# a compatibility review for the selected k3s/Kubernetes version.
CILIUM_VERSION="${CILIUM_VERSION:-1.16.5}"
K8S_SERVICE_HOST="${K8S_SERVICE_HOST:-127.0.0.1}"
K8S_SERVICE_PORT="${K8S_SERVICE_PORT:-6443}"
HELM_TIMEOUT="${HELM_TIMEOUT:-10m}"

helm repo add cilium https://helm.cilium.io --force-update
helm repo update cilium

helm upgrade --install cilium cilium/cilium \
  --namespace kube-system \
  --version "${CILIUM_VERSION}" \
  --values "${values_file}" \
  --set-string "k8sServiceHost=${K8S_SERVICE_HOST}" \
  --set "k8sServicePort=${K8S_SERVICE_PORT}" \
  --wait \
  --timeout "${HELM_TIMEOUT}"

echo "Cilium ${CILIUM_VERSION} installed; run config/k3s/validate.sh to verify readiness"
