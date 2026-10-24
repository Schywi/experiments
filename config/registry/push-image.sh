#!/usr/bin/env bash
#
# ROOTLESS push of a locally-built (buildah/podman) image into the in-cluster
# registry, so k3s can pull it with no privileged import step.
#
#   config/registry/push-image.sh llama-server
#     localhost/llama-server:local   ->  127.0.0.1:5000/llama-server:local
#
#   config/registry/push-image.sh llama-server:local my-name
#     localhost/llama-server:local   ->  127.0.0.1:5000/my-name:local
#
# buildah tags a bare name as localhost/<name>, so the source ref always
# carries the localhost/ prefix.
set -euo pipefail

registry="${REGISTRY:-127.0.0.1:5000}"

usage() {
  echo "usage: $0 <image>[:<tag>] [<registry-name>]" >&2
  echo "  <image>          local buildah image name (stored as localhost/<image>)" >&2
  echo "  <registry-name>  name to push under (default: <image>)" >&2
  exit 2
}
[[ $# -ge 1 && $# -le 2 ]] || usage

ref="$1"
name="${ref%%:*}"
tag="local"
[[ "${ref}" == *:* ]] && tag="${ref##*:}"
dest_name="${2:-${name}}"

src="localhost/${name}:${tag}"
dest="${registry}/${dest_name}:${tag}"

command -v buildah >/dev/null 2>&1 || { echo "buildah is required" >&2; exit 1; }

echo "push ${src} -> ${dest}"
# Plain HTTP to a loopback registry; the flag also covers setups without the
# user registries.conf drop-in.
buildah push --tls-verify=false "${src}" "docker://${dest}"
echo "pushed ${dest}"
