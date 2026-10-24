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

kubectl --namespace kube-system rollout status deployment/hubble-relay --timeout="${KUBECTL_TIMEOUT}"
kubectl --namespace kube-system rollout status deployment/hubble-ui --timeout="${KUBECTL_TIMEOUT}"

# Hubble UI is exposed as a LoadBalancer (Cilium LB IPAM), as in the k3d stack.
hubble_type="$(kubectl --namespace kube-system get service/hubble-ui --output=jsonpath='{.spec.type}')"
[[ "${hubble_type}" == "LoadBalancer" ]] || {
  echo "hubble-ui Service is ${hubble_type}, expected LoadBalancer" >&2
  exit 1
}
kubectl get ciliumloadbalancerippool lan-pool >/dev/null

# Argo CD is headless/no-auth and exposed exactly like the k3d profile: a
# LoadBalancer Service (Cilium LB IPAM) reached through the shared ingress.
if kubectl get namespace argocd >/dev/null 2>&1; then
  while IFS= read -r deployment; do
    [[ -n "${deployment}" ]] || continue
    kubectl --namespace argocd rollout status "${deployment}" --timeout="${KUBECTL_TIMEOUT}"
  done < <(kubectl --namespace argocd get deployments --output=name)

  service_type="$(kubectl --namespace argocd get service/argocd-server \
    --output=jsonpath='{.spec.type}')"
  [[ "${service_type}" == "LoadBalancer" ]] || {
    echo "argocd-server Service is ${service_type}, expected LoadBalancer" >&2
    exit 1
  }
  kubectl --namespace argocd get ingress/argocd-server >/dev/null
fi

# A plain Pod must schedule, get an IP, and reach the internet through Cilium.
kubectl delete pod k3s-net-smoke --ignore-not-found >/dev/null 2>&1 || true
kubectl run k3s-net-smoke \
  --image=curlimages/curl:latest \
  --restart=Never \
  --rm -i --command -- curl -fsS --max-time 15 https://example.com -o /dev/null

kubectl --namespace kube-system get pods -l k8s-app=cilium
echo "Native k3s platform validated"
