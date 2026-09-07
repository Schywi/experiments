#!/usr/bin/env bash

set -euo pipefail

command -v helm >/dev/null 2>&1 || { echo "helm is required" >&2; exit 1; }
command -v kubectl >/dev/null 2>&1 || { echo "kubectl is required" >&2; exit 1; }

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
version="v0.14.9"
helm_timeout="${HELM_TIMEOUT:-10m}"

helm repo add metallb https://metallb.github.io/metallb --force-update
helm repo update metallb

# This profile has one k3d server and no agents. A hostNetwork speaker cannot
# reach the Kubernetes ClusterIP in this kube-proxy-free k3d network, so give
# it the node-local API endpoint before waiting for the speaker rollout.
helm upgrade --install metallb metallb/metallb \
  --namespace metallb-system \
  --create-namespace \
  --version "${version#v}" \
  --set controller.image.repository=metallb-controller \
  --set controller.image.tag="${version}" \
  --set controller.image.pullPolicy=Never \
  --set speaker.image.repository=metallb-speaker \
  --set speaker.image.tag="${version}" \
  --set speaker.image.pullPolicy=Never \
  --set speaker.frr.enabled=false \
  --timeout "${helm_timeout}"

kubectl --namespace metallb-system set env daemonset/metallb-speaker \
  KUBERNETES_SERVICE_HOST=127.0.0.1 \
  KUBERNETES_SERVICE_PORT=6443

kubectl --namespace metallb-system rollout status deployment/metallb-controller \
  --timeout="${helm_timeout}"
kubectl --namespace metallb-system rollout status daemonset/metallb-speaker \
  --timeout="${helm_timeout}"

echo "MetalLB ${version} is ready"
