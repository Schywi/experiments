#!/usr/bin/env bash

set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
cluster_name="${K3D_CLUSTER_NAME:-cilium-lab}"

trap 'exit 130' INT
trap 'exit 143' TERM

# Start from a clean cluster when Tilt boots. The cluster remains available
# after bootstrap failure or when Tilt exits; delete it explicitly if desired.
K3D_CLUSTER_NAME="${cluster_name}" "${script_dir}/delete.sh"
K3D_CLUSTER_NAME="${cluster_name}" "${script_dir}/bootstrap.sh"

echo "Tilt is using k3d cluster '${cluster_name}'. Stop Tilt to preserve it."
while true; do
  sleep 3600 &
  wait $!
done
