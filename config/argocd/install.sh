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
values_file="${ARGOCD_VALUES_FILE:-${script_dir}/values.yaml}"

[[ -f "${values_file}" ]] || {
  echo "Argo CD values file not found: ${values_file}" >&2
  exit 1
}

# Pin the chart and review this value with the Kubernetes compatibility matrix
# before upgrading. The chart is community-maintained by the Argo project.
ARGOCD_CHART_VERSION="${ARGOCD_CHART_VERSION:-10.8.0}"
ARGOCD_RELEASE="${ARGOCD_RELEASE:-argocd}"
ARGOCD_NAMESPACE="${ARGOCD_NAMESPACE:-argocd}"
HELM_TIMEOUT="${HELM_TIMEOUT:-10m}"

helm repo add argo https://argoproj.github.io/argo-helm --force-update
helm repo update argo

helm upgrade --install "${ARGOCD_RELEASE}" argo/argo-cd \
  --namespace "${ARGOCD_NAMESPACE}" \
  --create-namespace \
  --version "${ARGOCD_CHART_VERSION}" \
  --values "${values_file}" \
  --wait \
  --timeout "${HELM_TIMEOUT}"

kubectl --namespace "${ARGOCD_NAMESPACE}" rollout status \
  deployment/"${ARGOCD_RELEASE}"-server --timeout="${HELM_TIMEOUT}"

echo "Argo CD ${ARGOCD_CHART_VERSION} installed in namespace ${ARGOCD_NAMESPACE}"
