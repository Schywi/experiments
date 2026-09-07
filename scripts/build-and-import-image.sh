#!/usr/bin/env bash

set -euo pipefail

usage() {
  echo "usage: $0 <expected-image-reference> <build-context>" >&2
  exit 2
}

[[ $# -eq 2 ]] || usage
image_ref="$1"
build_context="$2"

command -v docker >/dev/null 2>&1 || { echo "docker is required" >&2; exit 1; }
command -v k3d >/dev/null 2>&1 || { echo "k3d is required" >&2; exit 1; }

cluster_name="${K3D_CLUSTER_NAME:-cilium-lab}"
container_name="k3d-${cluster_name}-server-0"

[[ -n "${image_ref}" && -d "${build_context}" ]] || usage

docker build \
  --pull \
  --no-cache \
  --file "${build_context}/Containerfile" \
  --tag "${image_ref}" \
  "${build_context}"
docker image inspect "${image_ref}" >/dev/null
k3d image import "${image_ref}" --cluster "${cluster_name}"

short_image_ref="${image_ref#docker.io/}"
library_image_ref="${short_image_ref}"
if [[ "${short_image_ref}" != */* ]]; then
  library_image_ref="docker.io/library/${short_image_ref}"
fi

docker exec "${container_name}" sh -c \
  "ctr -n k8s.io images ls -q | grep -Fx -e '${image_ref}' -e '${short_image_ref}' -e '${library_image_ref}'" >/dev/null || {
  echo "k3d runtime does not contain imported image ${image_ref}" >&2
  exit 1
}

echo "Built and imported ${image_ref} into ${cluster_name}"
