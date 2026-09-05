#!/usr/bin/env bash

# Prepare the existing k3d server to run RuntimeClass "wasmtime" workloads.
# This script is intentionally not called by Tilt or bootstrap. It mutates the
# current node and requires an explicit --restart-node to restart its k3s
# process through Docker. Do not run it while the Cilium repair is in progress.

set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=versions.env
source "${script_dir}/versions.env"

cluster_name="${K3D_CLUSTER_NAME:-cilium-lab}"
node_name="k3d-${cluster_name}-server-0"
restart_node=false

if (($# > 1)); then
  echo "usage: $0 [--restart-node]" >&2
  exit 2
fi

if (($# == 1)); then
  if [[ "$1" != "--restart-node" ]]; then
    echo "usage: $0 [--restart-node]" >&2
    exit 2
  fi
  restart_node=true
fi

for command in curl docker find sha256sum tar; do
  command -v "${command}" >/dev/null 2>&1 || {
    echo "${command} is required" >&2
    exit 1
  }
done

if [[ "$(docker inspect --format '{{.State.Running}}' "${node_name}" 2>/dev/null || true)" != "true" ]]; then
  echo "running k3d server '${node_name}' was not found" >&2
  exit 1
fi

node_architecture="$(docker inspect --format '{{.Architecture}}' "${node_name}")"
if [[ "${node_architecture}" != "amd64" ]]; then
  echo "this pinned shim supports amd64 only; found '${node_architecture}'" >&2
  exit 1
fi

temporary_dir="$(mktemp -d)"
trap 'rm -rf -- "${temporary_dir}"' EXIT
archive_path="${temporary_dir}/${RUNWASI_WASMTIME_ARCHIVE}"
extracted_dir="${temporary_dir}/extracted"

curl --fail --silent --show-error --location \
  --output "${archive_path}" \
  "${RUNWASI_WASMTIME_URL}"
printf '%s  %s\n' "${RUNWASI_WASMTIME_SHA256}" "${archive_path}" | sha256sum --check --status || {
  echo "runwasi Wasmtime shim checksum verification failed" >&2
  exit 1
}

mkdir -p "${extracted_dir}"
tar --extract --gzip --file "${archive_path}" --directory "${extracted_dir}"
shim_path="$(find "${extracted_dir}" -type f -name containerd-shim-wasmtime-v1 -print -quit)"
if [[ -z "${shim_path}" ]]; then
  echo "release archive did not contain containerd-shim-wasmtime-v1" >&2
  exit 1
fi

shim_directory="$(docker exec "${node_name}" sh -ec 'dirname "$(command -v containerd-shim-runc-v2)"')"
docker cp "${shim_path}" "${node_name}:${shim_directory}/containerd-shim-wasmtime-v1"
docker exec "${node_name}" chmod 0755 "${shim_directory}/containerd-shim-wasmtime-v1"

# k3s owns the generated config.toml. Its supported customization point is a
# template which starts by rendering k3s's built-in base template. This k3d
# baseline uses containerd 1.7, so config.toml.tmpl is the applicable template.
template_path='/var/lib/rancher/k3s/agent/etc/containerd/config.toml.tmpl'
fragment_marker='# bounded-worm-lab Wasmtime runtime'
runtime_fragment="$(<"${script_dir}/containerd-runtime.toml")"
docker exec -i "${node_name}" sh -s -- "${template_path}" "${fragment_marker}" "${runtime_fragment}" <<'NODE_SCRIPT'
set -eu
template_path="$1"
fragment_marker="$2"
runtime_fragment="$3"

mkdir -p "$(dirname "${template_path}")"
if [ ! -f "${template_path}" ]; then
  printf '%s\n\n' '{{ template "base" . }}' >"${template_path}"
fi
if ! grep -Fqx "${fragment_marker}" "${template_path}"; then
  {
    printf '\n%s\n' "${fragment_marker}"
    printf '%s\n' "${runtime_fragment}"
  } >>"${template_path}"
fi
NODE_SCRIPT

echo "Installed ${CONTAINERD_RUNTIME_HANDLER} shim ${RUNWASI_WASMTIME_VERSION} and configured the k3s containerd template on ${node_name}."
if [[ "${restart_node}" == true ]]; then
  docker restart "${node_name}" >/dev/null
  echo "Restarted ${node_name}; wait for the existing platform to become healthy before applying RuntimeClass resources."
else
  echo "The active containerd process has not been restarted. Run '$0 --restart-node' only after the Cilium work is complete."
fi
