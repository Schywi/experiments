#!/usr/bin/env bash

set -euo pipefail

command -v k3d >/dev/null 2>&1 || {
  echo "k3d is required" >&2
  exit 1
}

CLUSTER_NAME="${K3D_CLUSTER_NAME:-cilium-lab}"
k3d cluster delete "${CLUSTER_NAME}"
