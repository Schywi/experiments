#!/usr/bin/env bash

set -euo pipefail

command -v docker >/dev/null 2>&1 || { echo "docker is required" >&2; exit 1; }
command -v k3d >/dev/null 2>&1 || { echo "k3d is required" >&2; exit 1; }

CLUSTER_NAME="${K3D_CLUSTER_NAME:-cilium-lab}"
images=(
  rancher/mirrored-pause:3.6
  quay.io/cilium/cilium:v1.13.4
  quay.io/cilium/hubble-relay:v1.13.4
  quay.io/cilium/hubble-ui:v0.13.2
  quay.io/cilium/hubble-ui-backend:v0.13.2
)

for image in "${images[@]}"; do
  docker image inspect "${image}" >/dev/null 2>&1 || {
    echo "required local image is missing: ${image}" >&2
    exit 1
  }
done

k3d image import "${images[@]}" --cluster "${CLUSTER_NAME}"
