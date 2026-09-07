#!/usr/bin/env bash

set -euo pipefail

command -v kubectl >/dev/null 2>&1 || {
  echo "kubectl is required" >&2
  exit 1
}
command -v curl >/dev/null 2>&1 || {
  echo "curl is required" >&2
  exit 1
}

KUBECTL_TIMEOUT="${KUBECTL_TIMEOUT:-5m}"
HTTP_TIMEOUT="${HTTP_TIMEOUT:-10}"
HTTP_RETRIES="${HTTP_RETRIES:-30}"
HTTP_RETRY_DELAY="${HTTP_RETRY_DELAY:-2}"

kubectl wait --for=condition=Ready nodes --all --timeout="${KUBECTL_TIMEOUT}"
kubectl --namespace kube-system rollout status daemonset/cilium --timeout="${KUBECTL_TIMEOUT:-5m}"
kubectl --namespace kube-system rollout status deployment/cilium-operator --timeout="${KUBECTL_TIMEOUT:-5m}"
# The pinned Cilium v1.13.4 in the Pod does not support the newer --wait flag.
# Rollout status checks above already wait for the daemonset and operator.
kubectl --namespace kube-system exec daemonset/cilium -- cilium status

kubectl --namespace metallb-system rollout status deployment/metallb-controller \
  --timeout="${KUBECTL_TIMEOUT}"
kubectl --namespace metallb-system rollout status daemonset/metallb-speaker \
  --timeout="${KUBECTL_TIMEOUT}"

loadbalancer_ip=""
for ((attempt = 1; attempt <= HTTP_RETRIES; attempt++)); do
  loadbalancer_ip="$(kubectl --namespace kube-system get service/cilium-ingress \
    --output=jsonpath='{.status.loadBalancer.ingress[0].ip}' 2>/dev/null || true)"
  if [[ -n "${loadbalancer_ip}" ]]; then
    break
  fi
  if ((attempt < HTTP_RETRIES)); then
    sleep "${HTTP_RETRY_DELAY}"
  fi
done

if [[ -z "${loadbalancer_ip}" ]]; then
  echo "cilium-ingress has no MetalLB external IP" >&2
  exit 1
fi

case "${loadbalancer_ip}" in
  172.20.0.24[0-9]|172.20.0.250) ;;
  *)
    echo "cilium-ingress received unexpected external IP ${loadbalancer_ip}" >&2
    exit 1
    ;;
esac

if kubectl --namespace kube-system get deployment/cilium-hubble-relay >/dev/null 2>&1; then
  kubectl --namespace kube-system rollout status deployment/cilium-hubble-relay --timeout="${KUBECTL_TIMEOUT:-5m}"
fi

if kubectl --namespace kube-system get deployment/hubble-ui >/dev/null 2>&1; then
  kubectl --namespace kube-system rollout status deployment/hubble-ui --timeout="${KUBECTL_TIMEOUT}"
else
  echo "Hubble UI deployment is absent" >&2
  exit 1
fi

while IFS= read -r deployment; do
  [[ -n "${deployment}" ]] || continue
  kubectl --namespace argocd rollout status "${deployment}" --timeout="${KUBECTL_TIMEOUT}"
done < <(kubectl --namespace argocd get deployments --output=name)

require_ingress_backend() {
  local namespace="$1"
  local ingress_name="$2"
  local expected_service="$3"
  local expected_host="$4"
  local ingress_class
  local backend_service

  kubectl --namespace "${namespace}" get ingress/"${ingress_name}" >/dev/null
  ingress_class="$(kubectl --namespace "${namespace}" get ingress/"${ingress_name}" \
    --output=jsonpath='{.spec.ingressClassName}')"
  if [[ "${ingress_class}" != "cilium" ]]; then
    echo "Ingress ${namespace}/${ingress_name} uses class ${ingress_class}, expected cilium" >&2
    return 1
  fi

  backend_service="$(kubectl --namespace "${namespace}" get ingress/"${ingress_name}" \
    --output=jsonpath='{.spec.rules[0].http.paths[0].backend.service.name}')"
  if [[ "${backend_service}" != "${expected_service}" ]]; then
    echo "Ingress ${namespace}/${ingress_name} targets ${backend_service}, expected ${expected_service}" >&2
    return 1
  fi

  local ingress_host
  ingress_host="$(kubectl --namespace "${namespace}" get ingress/"${ingress_name}" \
    --output=jsonpath='{.spec.rules[0].host}')"
  if [[ "${ingress_host}" != "${expected_host}" ]]; then
    echo "Ingress ${namespace}/${ingress_name} uses host ${ingress_host}, expected ${expected_host}" >&2
    return 1
  fi
}

assert_argocd_contract() {
  local service_type
  local insecure
  local disable_auth
  local admin_enabled

  kubectl --namespace argocd get namespace/argocd >/dev/null
  service_type="$(kubectl --namespace argocd get service/argocd-server \
    --output=jsonpath='{.spec.type}')"
  insecure="$(kubectl --namespace argocd get configmap/argocd-cmd-params-cm \
    --output=jsonpath='{.data.server\.insecure}')"
  disable_auth="$(kubectl --namespace argocd get configmap/argocd-cmd-params-cm \
    --output=jsonpath='{.data.server\.disable\.auth}')"
  admin_enabled="$(kubectl --namespace argocd get configmap/argocd-cm \
    --output=jsonpath='{.data.admin\.enabled}')"

  [[ "${service_type}" == "ClusterIP" ]] || {
    echo "argocd-server Service is ${service_type}, expected ClusterIP" >&2
    return 1
  }
  [[ "${insecure}" == "true" ]] || {
    echo "Argo CD server.insecure is ${insecure}, expected true" >&2
    return 1
  }
  [[ "${disable_auth}" == "true" ]] || {
    echo "Argo CD server.disable.auth is ${disable_auth}, expected true" >&2
    return 1
  }
  [[ "${admin_enabled}" == "false" ]] || {
    echo "Argo CD admin.enabled is ${admin_enabled}, expected false" >&2
    return 1
  }
}

check_http_route() {
  local route_name="$1"
  local host="$2"
  local attempt

  for ((attempt = 1; attempt <= HTTP_RETRIES; attempt++)); do
    if curl --fail --silent --show-error --location --max-time "${HTTP_TIMEOUT}" \
      --header "Host: ${host}" "http://${loadbalancer_ip}/" >/dev/null; then
      return 0
    fi
    if ((attempt < HTTP_RETRIES)); then
      sleep "${HTTP_RETRY_DELAY}"
    fi
  done

  echo "${route_name} ingress is unreachable at http://${loadbalancer_ip}/ (Host: ${host})" >&2
  return 1
}

assert_argocd_contract
require_ingress_backend kube-system hubble-ui hubble-ui localhost
require_ingress_backend argocd argocd-server argocd-server argocd.localhost
check_http_route "Hubble UI" localhost
check_http_route "Argo CD" argocd.localhost

kubectl --namespace kube-system get pods -l k8s-app=cilium
kubectl --namespace kube-system get service/cilium-ingress -o wide
