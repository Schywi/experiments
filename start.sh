#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
cluster_name="${K3D_CLUSTER_NAME:-cilium-lab}"
lock_file="/tmp/experiments-${cluster_name}.start.lock"

command -v flock >/dev/null 2>&1 || { echo "flock is required" >&2; exit 1; }

exec 9>"${lock_file}"
flock -n 9 || {
  echo "another start operation is already running for cluster '${cluster_name}'" >&2
  exit 1
}

export K3D_CLUSTER_NAME="${cluster_name}"

run_stage() {
  local name="$1"
  shift
  printf '\n==> %s\n' "${name}"
  "$@"
  printf '<== %s complete\n' "${name}"
}

run_stage "delete cluster" "${repo_root}/config/k3d/delete.sh"
run_stage "create cluster" "${repo_root}/config/k3d/create.sh"
run_stage "configure node DNS" "${repo_root}/config/k3d/configure-node-dns.sh"
run_stage "import platform images" "${repo_root}/config/k3d/import-images.sh"
run_stage "install Cilium" "${repo_root}/config/k3d/install-cilium.sh"
run_stage "install Sealed Secrets" "${repo_root}/config/sealed-secrets/install.sh"
run_stage "install Argo CD" "${repo_root}/config/argocd/install.sh"
run_stage "build and import MetalLB images" "${repo_root}/config/metallb/build-and-import.sh"
run_stage "install MetalLB" "${repo_root}/config/metallb/install.sh"
run_stage "configure MetalLB" "${repo_root}/config/metallb/configure.sh"
run_stage "enable Cilium ingress" "${repo_root}/config/k3d/enable-metallb-ingress.sh"
run_stage "install Argo CD ingress" "${repo_root}/config/argocd/install-ingress.sh"
run_stage "validate platform" "${repo_root}/config/k3d/validate.sh"
run_stage "build and deploy Worm" "${repo_root}/scripts/deploy-worm.sh"

printf '\nAll platform and Worm stages completed successfully.\n'
