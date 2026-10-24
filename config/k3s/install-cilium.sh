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
repo_root="$(cd -- "${script_dir}/../.." && pwd)"
values_file="${script_dir}/cilium-values.yaml"
lb_ipam_file="${script_dir}/cilium-lb-ipam.yaml"
hubble_service_file="${repo_root}/config/helm/cilium/hubble-ui-service.yaml"
hubble_ingress_file="${repo_root}/config/helm/cilium/hubble-ui-ingress.yaml"
# k3s writes its admin kubeconfig here.
export KUBECONFIG="${KUBECONFIG:-/etc/rancher/k3s/k3s.yaml}"

for required_file in "${values_file}" "${lb_ipam_file}" "${hubble_service_file}" "${hubble_ingress_file}"; do
  [[ -f "${required_file}" ]] || {
    echo "required file not found: ${required_file}" >&2
    exit 1
  }
done

# Keep the installer and values.yaml on the same Cilium release.
CILIUM_VERSION="${CILIUM_VERSION:-1.20.1}"
HELM_TIMEOUT="${HELM_TIMEOUT:-10m}"

helm repo add cilium https://helm.cilium.io --force-update
helm repo update cilium

# k8sServiceHost/k8sServicePort come from cilium-values.yaml for this profile.
# --force-conflicts: this script also applies hubble-ui-service.yaml below, so on
# a re-run the chart's hubble-ui Service conflicts with the kubectl-applied one
# (field manager "kubectl-client-side-apply"). Forcing the chart's server-side
# apply keeps re-runs of this script idempotent.
helm upgrade --install cilium cilium/cilium \
  --namespace kube-system \
  --version "${CILIUM_VERSION}" \
  --values "${values_file}" \
  --force-conflicts \
  --wait \
  --timeout "${HELM_TIMEOUT}"

# Enabling the ingress controller and L2 announcements changes the Cilium
# ConfigMap but does NOT roll the DaemonSet, so agents keep the stale config.
# Restart both agent and operator so the new config (and the Envoy-config CRDs
# the operator registers) actually take effect. This mirrors the k3d profile's
# enable-metallb-ingress.sh.
kubectl --namespace kube-system rollout restart daemonset/cilium
kubectl --namespace kube-system rollout restart deployment/cilium-operator
kubectl --namespace kube-system rollout status daemonset/cilium --timeout="${HELM_TIMEOUT}"
kubectl --namespace kube-system rollout status deployment/cilium-operator --timeout="${HELM_TIMEOUT}"

# Cilium provides LoadBalancer IPs (LB IPAM + L2 announcements); no MetalLB.
kubectl apply --filename "${lb_ipam_file}"

# Expose Hubble UI as a LoadBalancer and route it through the shared ingress
# (same as the k3d profile).
kubectl apply --filename "${hubble_service_file}"
kubectl apply --filename "${hubble_ingress_file}"

echo "Cilium ${CILIUM_VERSION} installed with Hubble Relay/UI, ingress, and Cilium LB IPAM"
