#!/usr/bin/env bash

set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
cluster_name="${K3D_CLUSTER_NAME:-cilium-lab}"

trap 'exit 130' INT
trap 'exit 143' TERM

# create.sh is idempotent: it creates the cluster when absent and validates
# the existing cluster when present. Destruction remains an explicit action
# through config/k3d/delete.sh.
K3D_CLUSTER_NAME="${cluster_name}" "${script_dir}/bootstrap.sh"

echo "Tilt is using k3d cluster '${cluster_name}'. Stop Tilt to preserve it."
while true; do
  sleep 3600 &
  wait $!
done
