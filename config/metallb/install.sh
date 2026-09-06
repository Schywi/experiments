#!/usr/bin/env bash

set -euo pipefail

command -v helm >/dev/null 2>&1 || { echo "helm is required" >&2; exit 1; }
command -v kubectl >/dev/null 2>&1 || { echo "kubectl is required" >&2; exit 1; }

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
version="v0.14.9"
helm_timeout="${HELM_TIMEOUT:-10m}"

"${script_dir}/build-and-import.sh"

helm repo add metallb https://metallb.github.io/metallb --force-update
helm repo update metallb

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
  --wait \
  --timeout "${helm_timeout}"

kubectl --namespace metallb-system rollout status deployment/metallb-controller \
  --timeout="${helm_timeout}"
kubectl --namespace metallb-system rollout status daemonset/metallb-speaker \
  --timeout="${helm_timeout}"

echo "MetalLB ${version} is ready"
