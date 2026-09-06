#!/usr/bin/env bash
# Build a Tilt-addressed worker image locally and import it into k3d.
# The component artifact is deliberately required: this script never pulls or
# pushes an image as a substitute for the selected local runtime package.

set -euo pipefail

usage() {
  echo "usage: $0 <expected-image-reference>" >&2
  echo "example: $0 docker.io/local/worm-worker:dev" >&2
  exit 2
}

[[ $# -eq 1 && -n "$1" ]] || usage

command -v docker >/dev/null 2>&1 || { echo "docker is required" >&2; exit 1; }
command -v k3d >/dev/null 2>&1 || { echo "k3d is required" >&2; exit 1; }

image_ref="$1"
cluster_name="${K3D_CLUSTER_NAME:-cilium-lab}"
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
component_path="${script_dir}/dist/worm.component.wasm"

[[ -s "${component_path}" ]] || {
  echo "missing worker component: ${component_path}; build the pinned Lua/WASI adapter first" >&2
  exit 1
}

if ! k3d cluster list "${cluster_name}" --no-headers | awk -v cluster="${cluster_name}" '$1 == cluster { found = 1 } END { exit !found }'; then
  echo "k3d cluster '${cluster_name}' does not exist; create it before importing ${image_ref}" >&2
  exit 1
fi

docker build --file "${script_dir}/Containerfile" --tag "${image_ref}" "${script_dir}"
docker image inspect "${image_ref}" >/dev/null
k3d image import "${image_ref}" --cluster "${cluster_name}"
