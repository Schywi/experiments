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
values_file="${SEALED_SECRETS_VALUES_FILE:-${script_dir}/values.yaml}"

[[ -f "${values_file}" ]] || {
  echo "Sealed Secrets values file not found: ${values_file}" >&2
  exit 1
}

# Pin the chart and review this value with the controller compatibility matrix
# before upgrading. The chart is community-maintained by the Bitnami project.
SEALED_SECRETS_CHART_VERSION="${SEALED_SECRETS_CHART_VERSION:-2.20.0}"
SEALED_SECRETS_RELEASE="${SEALED_SECRETS_RELEASE:-sealed-secrets}"
SEALED_SECRETS_NAMESPACE="${SEALED_SECRETS_NAMESPACE:-kube-system}"
HELM_TIMEOUT="${HELM_TIMEOUT:-10m}"

helm repo add sealed-secrets https://bitnami.github.io/sealed-secrets --force-update
helm repo update sealed-secrets

helm upgrade --install "${SEALED_SECRETS_RELEASE}" sealed-secrets/sealed-secrets \
  --namespace "${SEALED_SECRETS_NAMESPACE}" \
  --version "${SEALED_SECRETS_CHART_VERSION}" \
  --values "${values_file}" \
  --wait \
  --timeout "${HELM_TIMEOUT}"

kubectl --namespace "${SEALED_SECRETS_NAMESPACE}" rollout status \
  deployment/sealed-secrets-controller --timeout="${HELM_TIMEOUT}"

echo "Sealed Secrets ${SEALED_SECRETS_CHART_VERSION} installed in namespace ${SEALED_SECRETS_NAMESPACE}"
