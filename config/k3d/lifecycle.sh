#!/usr/bin/env bash

set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
cluster_name="${K3D_CLUSTER_NAME:-cilium-lab}"
delete_on_exit="${TILT_DELETE_CLUSTER_ON_EXIT:-false}"

cleanup() {
  local status=$?
  trap - EXIT INT TERM
  if [[ "${delete_on_exit}" == "true" ]]; then
    K3D_CLUSTER_NAME="${cluster_name}" "${script_dir}/delete.sh" || true
  else
    echo "Preserving k3d cluster '${cluster_name}'; set TILT_DELETE_CLUSTER_ON_EXIT=true for explicit deletion." >&2
  fi
  exit "${status}"
}

trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# Reuse an existing cluster when present. Destruction is an explicit operator
# choice via TILT_DELETE_CLUSTER_ON_EXIT=true, never an implicit failure path.
K3D_CLUSTER_NAME="${cluster_name}" "${script_dir}/bootstrap.sh"

echo "Tilt is using k3d cluster '${cluster_name}'. Stop Tilt to preserve it."
while true; do
  sleep 3600 &
  wait $!
done
