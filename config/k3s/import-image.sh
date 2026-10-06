#!/usr/bin/env bash
#
# Import a locally-built (buildah/podman) image into k3s's containerd.
#
#   config/k3s/import-image.sh localhost/assistant:local assistant:local
#
# Two different image stores are in play:
#   * buildah/podman write to ~/.local/share/containers/storage
#   * k3s reads its own containerd store (/var/lib/rancher/k3s/agent/containerd)
# Nothing bridges them, so "built" is not "in the cluster" -- you must export
# and import. buildah also tags local images as "localhost/<name>", while k3s
# resolves a bare "<name>:<tag>" to "docker.io/library/<name>:<tag>". This
# script bridges all of that: retag to the k8s name, export, import, verify.
set -euo pipefail

usage() { echo "usage: $0 <built-ref> <k8s-ref>   (e.g. localhost/assistant:local assistant:local)" >&2; exit 2; }
[[ $# -eq 2 ]] || usage
built_ref="$1"
k8s_ref="$2"

command -v buildah >/dev/null 2>&1 || { echo "buildah is required" >&2; exit 1; }
command -v k3s >/dev/null 2>&1 || { echo "k3s is required" >&2; exit 1; }

tar="/tmp/$(echo "${k8s_ref}" | tr '/:' '__').tar"

echo "retag ${built_ref} -> ${k8s_ref}"
buildah tag "${built_ref}" "${k8s_ref}"

echo "export ${k8s_ref} -> ${tar}"
buildah push "${k8s_ref}" "oci-archive:${tar}"

echo "import ${tar} into k3s containerd"
sudo k3s ctr images import "${tar}"

if ! sudo k3s ctr images ls -q | grep -Fxq "${k8s_ref}"; then
  echo "ERROR: ${k8s_ref} not present after import; inspect: sudo k3s ctr images ls | grep -i '${k8s_ref%%:*}'" >&2
  exit 1
fi
echo "OK: ${k8s_ref} is available to k3s"
