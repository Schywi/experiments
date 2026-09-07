#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
cluster_name="${K3D_CLUSTER_NAME:-cilium-lab}"
lock_file="/tmp/experiments-${cluster_name}.start.lock"

command -v tilt >/dev/null 2>&1 || { echo "tilt is required" >&2; exit 1; }
command -v flock >/dev/null 2>&1 || { echo "flock is required" >&2; exit 1; }

exec 9>"${lock_file}"
flock -n 9 || {
  echo "another start operation is already running for cluster '${cluster_name}'" >&2
  exit 1
}

export K3D_CLUSTER_NAME="${cluster_name}"
"${repo_root}/config/k3d/delete.sh"
"${repo_root}/config/k3d/create.sh"

exec tilt up "$@"
