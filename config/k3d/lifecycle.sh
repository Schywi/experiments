#!/usr/bin/env bash

set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
cluster_name="${K3D_CLUSTER_NAME:-cilium-lab}"

trap 'exit 130' INT
trap 'exit 143' TERM

# Every Tilt startup starts from a clean, reproducible cluster. delete.sh is
# idempotent, so the absent-cluster case is safe.
K3D_CLUSTER_NAME="${cluster_name}" "${script_dir}/delete.sh"
K3D_CLUSTER_NAME="${cluster_name}" "${script_dir}/bootstrap.sh"

echo "Tilt is using fresh k3d cluster '${cluster_name}'."
while true; do
  sleep 3600 &
  wait $!
done
