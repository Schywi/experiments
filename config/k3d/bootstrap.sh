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

  # The Helm installer must finish before this function is called. These
  # checks make the ordering explicit and prevent an ingress from being
  # applied against a namespace or Service that does not exist yet.
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

"${script_dir}/create.sh"
"${script_dir}/configure-node-dns.sh"

bootstrap_status=0
argocd_status=1
cilium_status=1
if ! "${script_dir}/import-images.sh"; then
  echo "WARNING: local image import failed; continuing independent platform branches" >&2
  bootstrap_status=1
fi

# Cilium and Argo CD are sibling branches after k3d creation. Keep the Cilium
# Helm wait from blocking Argo CD when the node cannot yet pull or run images.
"${script_dir}/install-cilium.sh" &
cilium_pid=$!

"${repo_root}/config/argocd/install.sh" &
argocd_pid=$!

if wait "${argocd_pid}"; then
  argocd_status=0
else
  echo "WARNING: Argo CD bootstrap failed; inspect its Helm/pod events" >&2
  bootstrap_status=1
fi

if wait "${cilium_pid}"; then
  cilium_status=0
else
  echo "WARNING: Cilium bootstrap failed; Argo CD was not gated by it" >&2
  bootstrap_status=1
fi

if ((argocd_status == 0)); then
  if ! apply_argocd_ingress; then
    echo "WARNING: Argo CD Ingress was not applied" >&2
    bootstrap_status=1
  fi
fi

if ((cilium_status == 0)); then
  if ! "${script_dir}/validate.sh"; then
    echo "WARNING: platform ingress validation failed" >&2
    bootstrap_status=1
  fi
fi

if ((bootstrap_status == 0)); then
  echo "k3d, Cilium Ingress, and Argo CD are ready; Hubble UI is at http://localhost:8080/ and Argo CD is at http://argocd.localhost:8080/"
else
  echo "Platform bootstrap attempted all independent branches; inspect the warnings above" >&2
fi
exit "${bootstrap_status}"
