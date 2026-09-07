#!/usr/bin/env bash

set -euo pipefail

command -v docker >/dev/null 2>&1 || { echo "docker is required" >&2; exit 1; }
command -v k3d >/dev/null 2>&1 || { echo "k3d is required" >&2; exit 1; }

cluster_name="${K3D_CLUSTER_NAME:-cilium-lab}"
container_name="k3d-${cluster_name}-server-0"
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
version="v0.14.9"
controller_image="metallb-controller:${version}"
speaker_image="metallb-speaker:${version}"

docker build --pull --no-cache --file "${script_dir}/Dockerfile.controller" \
  --tag "${controller_image}" "${script_dir}"
docker build --pull --no-cache --file "${script_dir}/Dockerfile.speaker" \
  --tag "${speaker_image}" "${script_dir}"

k3d image import "${controller_image}" "${speaker_image}" --cluster "${cluster_name}"

for image in "${controller_image}" "${speaker_image}"; do
  short_image="${image#docker.io/}"
  library_image="docker.io/library/${short_image}"
  docker exec "${container_name}" sh -c \
    "ctr -n k8s.io images ls -q | grep -Fx -e '${image}' -e '${short_image}' -e '${library_image}'" >/dev/null || {
    echo "k3d runtime does not contain imported image ${image}" >&2
    exit 1
  }
done

echo "Imported local MetalLB ${version} images into k3d cluster ${cluster_name}"
