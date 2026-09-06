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

kubectl wait --for=condition=Ready nodes --all --timeout="${KUBECTL_TIMEOUT:-5m}"
kubectl --namespace kube-system rollout status daemonset/cilium --timeout="${KUBECTL_TIMEOUT:-5m}"
kubectl --namespace kube-system rollout status deployment/cilium-operator --timeout="${KUBECTL_TIMEOUT:-5m}"
kubectl --namespace kube-system exec daemonset/cilium -- cilium status --wait

if kubectl --namespace kube-system get deployment/cilium-hubble-relay >/dev/null 2>&1; then
  kubectl --namespace kube-system rollout status deployment/cilium-hubble-relay --timeout="${KUBECTL_TIMEOUT:-5m}"
fi

if kubectl --namespace kube-system get deployment/hubble-ui >/dev/null 2>&1; then
  kubectl --namespace kube-system rollout status deployment/hubble-ui --timeout="${KUBECTL_TIMEOUT:-5m}"
fi

kubectl --namespace kube-system get ingress/hubble-ui
curl --fail --silent --show-error --retry 30 --retry-delay 2 --max-time 10 http://localhost:8080/ >/dev/null

kubectl --namespace kube-system get pods -l k8s-app=cilium
