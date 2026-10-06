#!/usr/bin/env bash
#
# Install Cilium and Hubble on the native k3s host. Run after install-k3s.sh.

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
values_file="${script_dir}/cilium-values.yaml"
# k3s writes its admin kubeconfig here; the API is loopback-only.
export KUBECONFIG="${KUBECONFIG:-/etc/rancher/k3s/k3s.yaml}"

[[ -f "${values_file}" ]] || {
  echo "Cilium values file not found: ${values_file}" >&2
  exit 1
}

# Keep the installer and values.yaml on the same Cilium release.
CILIUM_VERSION="${CILIUM_VERSION:-1.20.1}"
HELM_TIMEOUT="${HELM_TIMEOUT:-10m}"

helm repo add cilium https://helm.cilium.io --force-update
helm repo update cilium

# k8sServiceHost/k8sServicePort come from cilium-values.yaml for this profile.
helm upgrade --install cilium cilium/cilium \
  --namespace kube-system \
  --version "${CILIUM_VERSION}" \
  --values "${values_file}" \
  --wait \
  --timeout "${HELM_TIMEOUT}"

echo "Cilium ${CILIUM_VERSION} installed with Hubble Relay and UI on the native k3s host"
