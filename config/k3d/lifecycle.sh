#!/usr/bin/env bash

set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
cluster_name="${K3D_CLUSTER_NAME:-cilium-lab}"

cleanup() {
  local status=$?
  trap - EXIT INT TERM
  K3D_CLUSTER_NAME="${cluster_name}" "${script_dir}/delete.sh" || true
  exit "${status}"
}

trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# Every Tilt lifecycle starts from a clean, reproducible k3d cluster.
K3D_CLUSTER_NAME="${cluster_name}" "${script_dir}/delete.sh"
K3D_CLUSTER_NAME="${cluster_name}" "${script_dir}/bootstrap.sh"

echo "Tilt owns k3d cluster '${cluster_name}'. Stop Tilt to delete it."
while true; do
  sleep 3600 &
  wait $!
done
