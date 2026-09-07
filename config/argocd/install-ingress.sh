#!/usr/bin/env bash

set -euo pipefail

command -v kubectl >/dev/null 2>&1 || { echo "kubectl is required" >&2; exit 1; }

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ingress_file="${script_dir}/argocd-ingress.yaml"
service_file="${script_dir}/argocd-service.yaml"
kubectl_timeout="${KUBECTL_TIMEOUT:-5m}"

[[ -f "${ingress_file}" ]] || {
  echo "Argo CD Ingress manifest not found: ${ingress_file}" >&2
  exit 1
}
[[ -f "${service_file}" ]] || {
  echo "Argo CD Service manifest not found: ${service_file}" >&2
  exit 1
}

kubectl wait --for=create namespace/argocd --timeout="${kubectl_timeout}"
kubectl apply --filename "${service_file}"
kubectl --namespace argocd get service/argocd-server >/dev/null

service_type="$(kubectl --namespace argocd get service/argocd-server --output=jsonpath='{.spec.type}')"
if [[ "${service_type}" != "LoadBalancer" ]]; then
  echo "refusing Argo CD Ingress: argocd-server Service is ${service_type}, expected LoadBalancer" >&2
  exit 1
fi

server_insecure="$(kubectl --namespace argocd get configmap/argocd-cmd-params-cm --output=jsonpath='{.data.server\.insecure}')"
if [[ "${server_insecure}" != "true" ]]; then
  echo "refusing Argo CD Ingress: server.insecure is not true" >&2
  exit 1
fi

disable_auth="$(kubectl --namespace argocd get configmap/argocd-cmd-params-cm --output=jsonpath='{.data.server\.disable\.auth}')"
if [[ "${disable_auth}" != "true" ]]; then
  echo "refusing Argo CD Ingress: server.disable.auth is not true" >&2
  exit 1
fi

kubectl apply --filename "${ingress_file}"
echo "Argo CD Ingress applied"
