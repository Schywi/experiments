#!/usr/bin/env bash

set -euo pipefail

command -v docker >/dev/null 2>&1 || { echo "docker is required" >&2; exit 1; }
command -v k3d >/dev/null 2>&1 || { echo "k3d is required" >&2; exit 1; }

cluster_name="${K3D_CLUSTER_NAME:-cilium-lab}"
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
version="v0.14.9"
controller_image="docker.io/local/metallb-controller:${version}"
speaker_image="docker.io/local/metallb-speaker:${version}"

docker build --file "${script_dir}/Dockerfile.controller" \
  --tag "${controller_image}" "${script_dir}"
docker build --file "${script_dir}/Dockerfile.speaker" \
  --tag "${speaker_image}" "${script_dir}"

k3d image import "${controller_image}" "${speaker_image}" --cluster "${cluster_name}"

echo "Imported local MetalLB ${version} images into k3d cluster ${cluster_name}"
