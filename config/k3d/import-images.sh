#!/usr/bin/env bash

set -euo pipefail

command -v docker >/dev/null 2>&1 || { echo "docker is required" >&2; exit 1; }
command -v k3d >/dev/null 2>&1 || { echo "k3d is required" >&2; exit 1; }

CLUSTER_NAME="${K3D_CLUSTER_NAME:-cilium-lab}"
images=(
  rancher/mirrored-pause:3.6
  cilium/cilium:v1.13.4
  cilium/operator-generic:v1.13.4
  cilium/hubble-relay:v1.13.4
  cilium/hubble-ui:v0.11.0
  cilium/hubble-ui-backend:v0.11.0
)

expected_digests=(
  ""
  sha256:bde8800d61aaad8b8451b10e247ac7bdeb7af187bb698f83d40ad75a38c1ee6b
  sha256:09ab77d324ef4d31f7d341f97ec5a2a4860910076046d57a2d61494d426c6301
  sha256:bac057a5130cf75adf5bc363292b1f2642c0c460ac9ff018fcae3daf64873871
  sha256:bcb369c47cada2d4257d63d3749f7f87c91dde32e010b223597306de95d1ecc8
  sha256:14c04d11f78da5c363f88592abae8d2ecee3cbe009f443ef11df6ac5f692d839
)

for index in "${!images[@]}"; do
  image="${images[${index}]}"
  docker image inspect "${image}" >/dev/null 2>&1 || {
    echo "required local image is missing: ${image}" >&2
    exit 1
  }

  expected_digest="${expected_digests[${index}]}"
  if [[ -n "${expected_digest}" ]]; then
    expected_reference="${image%:*}@${expected_digest}"
    docker image inspect --format '{{range .RepoDigests}}{{println .}}{{end}}' "${image}" | grep -Fxq "${expected_reference}" || {
      echo "local image digest does not match the pinned Docker Hub reference: ${expected_reference}" >&2
      exit 1
    }
  fi
done

k3d image import "${images[@]}" --cluster "${CLUSTER_NAME}"
