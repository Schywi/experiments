#!/usr/bin/env bash

set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
kubectl_bin="${KUBECTL_BIN:-kubectl}"
target_node="k3d-cilium-lab-server-0"

command -v "${kubectl_bin}" >/dev/null 2>&1 || {
  echo "kubectl is required" >&2
  exit 1
}

if ! "${kubectl_bin}" get namespace metallb-system >/dev/null 2>&1; then
  echo "MetalLB is not installed: namespace metallb-system is missing" >&2
  echo "Install MetalLB with approved Docker Hub images, then rerun this script" >&2
  exit 1
fi

for crd in ipaddresspools.metallb.io l2advertisements.metallb.io; do
  if ! "${kubectl_bin}" get crd "${crd}" >/dev/null 2>&1; then
    echo "MetalLB is not ready: CRD ${crd} is missing" >&2
    exit 1
  fi
done

if ! "${kubectl_bin}" get node "${target_node}" >/dev/null 2>&1; then
  echo "Required single node ${target_node} is missing" >&2
  exit 1
fi

echo "Applying MetalLB Layer 2 configuration from ${script_dir}"
"${kubectl_bin}" apply \
  --server-side \
  --field-manager=experiments-metallb \
  -k "${script_dir}"

echo "MetalLB Layer 2 configuration applied"
"${kubectl_bin}" get ipaddresspool -n metallb-system docker-host-pool
"${kubectl_bin}" get l2advertisement -n metallb-system docker-host-l2
