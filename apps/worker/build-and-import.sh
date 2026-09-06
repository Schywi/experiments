#!/usr/bin/env bash
# Build a Tilt-addressed native worker image locally and import it into k3d.

set -euo pipefail

usage() {
  echo "usage: $0 <expected-image-reference> [output-ref-file]" >&2
  echo "example: $0 worm-worker:tilt-build-abc123 /tmp/worm-worker-tilt-image-ref" >&2
  exit 2
}

[[ $# -eq 1 || $# -eq 2 ]] || usage
[[ -n "$1" ]] || usage

command -v docker >/dev/null 2>&1 || { echo "docker is required" >&2; exit 1; }
command -v k3d >/dev/null 2>&1 || { echo "k3d is required" >&2; exit 1; }

image_ref="$1"
output_ref_file="${2:-}"
cluster_name="${K3D_CLUSTER_NAME:-cilium-lab}"
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
if ! k3d cluster list "${cluster_name}" --no-headers | awk -v cluster="${cluster_name}" '$1 == cluster { found = 1 } END { exit !found }'; then
  echo "k3d cluster '${cluster_name}' does not exist; create it before importing ${image_ref}" >&2
  exit 1
fi

docker build --file "${script_dir}/Containerfile" --tag "${image_ref}" "${script_dir}"
docker image inspect "${image_ref}" >/dev/null
k3d image import "${image_ref}" --cluster "${cluster_name}"

container_image_ref="${image_ref}"
if [[ "${container_image_ref}" != */* ]]; then
  container_image_ref="docker.io/library/${container_image_ref}"
fi
docker exec "k3d-${cluster_name}-server-0" sh -c \
  "ctr -n k8s.io images ls -q | grep -Fx -- '${container_image_ref}'" >/dev/null || {
  echo "k3d runtime does not contain imported image ${image_ref}" >&2
  exit 1
}

if [[ -n "${output_ref_file}" ]]; then
  printf '%s\n' "${image_ref}" >"${output_ref_file}"
fi
