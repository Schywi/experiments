#!/usr/bin/env bash
# Build a Tilt-addressed regression image locally and import it into k3d.
# This script intentionally has no remote-registry push step.

set -euo pipefail

usage() {
  echo "usage: $0 <expected-image-reference> [output-ref-file]" >&2
  echo "example: $0 worm-regression:tilt-build-abc123 /tmp/worm-regression-tilt-image-ref" >&2
  exit 2
}

[[ $# -eq 1 || $# -eq 2 ]] || usage
[[ -n "$1" ]] || usage

command -v docker >/dev/null 2>&1 || { echo "docker is required" >&2; exit 1; }
command -v k3d >/dev/null 2>&1 || { echo "k3d is required" >&2; exit 1; }

IMAGE_REF="$1"
OUTPUT_REF_FILE="${2:-}"
CLUSTER_NAME="${K3D_CLUSTER_NAME:-cilium-lab}"
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

if ! k3d cluster list "$CLUSTER_NAME" --no-headers | awk -v cluster="$CLUSTER_NAME" '$1 == cluster { found = 1 } END { exit !found }'; then
  echo "k3d cluster '$CLUSTER_NAME' does not exist; create it before importing $IMAGE_REF" >&2
  exit 1
fi

docker build --file "$SCRIPT_DIR/Containerfile" --tag "$IMAGE_REF" "$SCRIPT_DIR"
docker image inspect "$IMAGE_REF" >/dev/null
k3d image import "$IMAGE_REF" --cluster "$CLUSTER_NAME"

container_image_ref="$IMAGE_REF"
if [[ "${container_image_ref}" != */* ]]; then
  container_image_ref="docker.io/library/${container_image_ref}"
fi
docker exec "k3d-${CLUSTER_NAME}-server-0" sh -c \
  "ctr -n k8s.io images ls -q | grep -Fx -- '${container_image_ref}'" >/dev/null || {
  echo "k3d runtime does not contain imported image ${IMAGE_REF}" >&2
  exit 1
}

if [[ -n "${OUTPUT_REF_FILE}" ]]; then
  printf '%s\n' "${IMAGE_REF}" >"${OUTPUT_REF_FILE}"
fi
