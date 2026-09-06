#!/usr/bin/env bash

set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "${script_dir}/../.." && pwd)"
argocd_ingress_file="${repo_root}/config/argocd/argocd-ingress.yaml"

apply_argocd_ingress() {
  local kubectl_timeout="${KUBECTL_TIMEOUT:-5m}"
  local service_type
  local params_configmap

  [[ -f "${argocd_ingress_file}" ]] || {
    echo "Argo CD Ingress manifest not found: ${argocd_ingress_file}" >&2
    return 1
  }

  kubectl wait --for=create namespace/argocd --timeout="${kubectl_timeout}"
  kubectl --namespace argocd get service/argocd-server >/dev/null

  service_type="$(kubectl --namespace argocd get service/argocd-server \
    --output=jsonpath='{.spec.type}')"
  if [[ "${service_type}" != "ClusterIP" ]]; then
    echo "refusing Argo CD Ingress: argocd-server Service is ${service_type}, expected ClusterIP" >&2
    return 1
  fi

  params_configmap="$(kubectl --namespace argocd get configmap/argocd-cmd-params-cm \
    --output=jsonpath='{.data.server\.insecure}')"
  if [[ "${params_configmap}" != "true" ]]; then
    echo "refusing Argo CD Ingress: server.insecure is not true" >&2
    return 1
  fi

  params_configmap="$(kubectl --namespace argocd get configmap/argocd-cmd-params-cm \
    --output=jsonpath='{.data.server\.disable\.auth}')"
  if [[ "${params_configmap}" != "true" ]]; then
    echo "refusing Argo CD Ingress: server.disable.auth is not true" >&2
    return 1
  fi

  kubectl apply --filename "${argocd_ingress_file}"
}

# Each stage is a hard gate. A fresh node never receives application workloads
# if an image import or platform component failed.
"${script_dir}/create.sh"
"${script_dir}/configure-node-dns.sh"
"${script_dir}/import-images.sh"
"${script_dir}/install-cilium.sh"
"${repo_root}/config/argocd/install.sh"
"${repo_root}/config/metallb/install.sh"
"${repo_root}/config/metallb/configure.sh"
"${script_dir}/enable-metallb-ingress.sh"
apply_argocd_ingress
"${script_dir}/validate.sh"

echo "k3d, MetalLB, Cilium Ingress, and Argo CD are ready; Hubble UI and Argo CD are available through the assigned LoadBalancer IP"
