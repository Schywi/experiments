#!/usr/bin/env bash

set -euo pipefail

command -v k3d >/dev/null 2>&1 || {
  echo "k3d is required" >&2
  exit 1
}

CLUSTER_NAME="${K3D_CLUSTER_NAME:-cilium-lab}"
if ! k3d cluster list --no-headers 2>/dev/null | awk '{print $1}' | grep -Fxq "${CLUSTER_NAME}"; then
  echo "k3d cluster '${CLUSTER_NAME}' is already absent"
  exit 0
fi

k3d cluster delete "${CLUSTER_NAME}"
