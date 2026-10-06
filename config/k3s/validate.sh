#!/usr/bin/env bash
#
# Validate the native-k3s platform: node, Cilium, Hubble Relay/UI, and (when
# present) headless Argo CD. Ends with a Pod scheduling and internet check.

set -euo pipefail

command -v kubectl >/dev/null 2>&1 || {
  echo "kubectl is required" >&2
  exit 1
}

KUBECTL_TIMEOUT="${KUBECTL_TIMEOUT:-5m}"
export KUBECONFIG="${KUBECONFIG:-/etc/rancher/k3s/k3s.yaml}"

kubectl wait --for=condition=Ready nodes --all --timeout="${KUBECTL_TIMEOUT}"
kubectl --namespace kube-system rollout status daemonset/cilium --timeout="${KUBECTL_TIMEOUT}"
kubectl --namespace kube-system rollout status deployment/cilium-operator --timeout="${KUBECTL_TIMEOUT}"
kubectl --namespace kube-system exec daemonset/cilium -- cilium status

kubectl --namespace kube-system rollout status deployment/cilium-hubble-relay --timeout="${KUBECTL_TIMEOUT}"
kubectl --namespace kube-system rollout status deployment/hubble-ui --timeout="${KUBECTL_TIMEOUT}"

# Argo CD is headless/no-auth and ClusterIP only: no ingress or LoadBalancer.
if kubectl get namespace argocd >/dev/null 2>&1; then
  while IFS= read -r deployment; do
    [[ -n "${deployment}" ]] || continue
    kubectl --namespace argocd rollout status "${deployment}" --timeout="${KUBECTL_TIMEOUT}"
  done < <(kubectl --namespace argocd get deployments --output=name)

  service_type="$(kubectl --namespace argocd get service/argocd-server \
    --output=jsonpath='{.spec.type}')"
  [[ "${service_type}" == "ClusterIP" ]] || {
    echo "argocd-server Service is ${service_type}, expected ClusterIP (no public exposure)" >&2
    exit 1
  }
fi

# A plain Pod must schedule, get an IP, and reach the internet through Cilium.
kubectl delete pod k3s-net-smoke --ignore-not-found >/dev/null 2>&1 || true
kubectl run k3s-net-smoke \
  --image=curlimages/curl:latest \
  --restart=Never \
  --rm -i --command -- curl -fsS --max-time 15 https://example.com -o /dev/null

kubectl --namespace kube-system get pods -l k8s-app=cilium
echo "Native k3s platform validated"
