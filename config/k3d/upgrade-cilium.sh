#!/usr/bin/env bash

set -euo pipefail

command -v helm >/dev/null 2>&1 || { echo "helm is required" >&2; exit 1; }
command -v kubectl >/dev/null 2>&1 || { echo "kubectl is required" >&2; exit 1; }
command -v docker >/dev/null 2>&1 || { echo "docker is required" >&2; exit 1; }

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "${script_dir}/../.." && pwd)"
values_file="${repo_root}/config/helm/cilium/values.yaml"
cluster_name="${K3D_CLUSTER_NAME:-cilium-lab}"
server_container="k3d-${cluster_name}-server-0"
helm_timeout="${HELM_TIMEOUT:-10m}"
server_ip="$(docker inspect --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "${server_container}" 2>/dev/null || true)"

[[ -n "${server_ip}" ]] || { echo "could not resolve the k3d server container IP" >&2; exit 1; }

helm repo add cilium https://helm.cilium.io --force-update
helm repo update cilium

versions=(1.13.18 1.14.19 1.15.19 1.16.19 1.17.18 1.18.13 1.19.7 1.20.1)
compatibility=1.13
for version in "${versions[@]}"; do
  printf '\n==> upgrade Cilium to %s\n' "${version}"
  kube_proxy_replacement=(--set kubeProxyReplacement=true)
  hubble_ui=(--set hubble.ui.enabled=false)
  if [[ "${version}" == 1.13.* ]]; then
    kube_proxy_replacement=(--set-string kubeProxyReplacement=strict)
  fi
  if [[ "${version}" == 1.20.1 ]]; then
    hubble_ui=()
  fi
  helm upgrade cilium cilium/cilium \
    --namespace kube-system \
    --version "${version}" \
    --values "${values_file}" \
    --set-string k8sServiceHost="${server_ip}" \
    --set-string k8sServicePort=6443 \
    --set-string upgradeCompatibility="${compatibility}" \
    "${kube_proxy_replacement[@]}" \
    "${hubble_ui[@]}" \
    --wait \
    --timeout "${helm_timeout}"
  kubectl --namespace kube-system rollout status daemonset/cilium --timeout="${helm_timeout}"
  kubectl --namespace kube-system rollout status deployment/cilium-operator --timeout="${helm_timeout}"
  compatibility="${version%.*}"
done
