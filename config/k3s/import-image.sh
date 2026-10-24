#!/usr/bin/env bash
#
# OFFLINE FALLBACK -- prefer config/registry/ (rootless, zero sudo).
#
# Import a locally-built (buildah/podman) image into k3s's containerd.
#
#   config/k3s/import-image.sh localhost/assistant:local assistant:local
#
# Why this exists:
#   * buildah/podman write to ~/.local/share/containers/storage; k3s reads its
#     own containerd store (/var/lib/rancher/k3s/agent/containerd). Nothing
#     bridges them, so "built" is not "in the cluster" -- export and import.
#   * buildah tags a bare name as localhost/<name>; k3s resolves a bare
#     "<name>:<tag>" to docker.io/library/<name>:<tag>. Import under the wrong
#     name and the Pod shows ErrImageNeverPull.
# This script retags to the fully-qualified k8s name, exports, imports, verifies.
#
# Run it AS YOUR USER; it escalates only the `k3s ctr images import` line via
# sudo. Never run the whole script through `sudo` -- see the guard below.
set -euo pipefail

# Root cause of the historical `Error: localhost/<name>: image not known`: the
# helper was invoked as `sudo bash import-image.sh ...`, so buildah consulted
# root's store (/var/lib/containers/storage) instead of the invoking user's
# rootless store (~/.local/share/containers/storage) and found nothing. Refuse
# up front rather than fail cryptically.
if [[ ${EUID} -eq 0 ]]; then
  echo "ERROR: do not run this helper as root/sudo." >&2
  echo "buildah images live in the invoking user's rootless store; run this as" >&2
  echo "your user and let it escalate only the k3s import line. For a zero-sudo" >&2
  echo "workflow, use config/registry/ instead." >&2
  exit 1
fi

usage() { echo "usage: $0 <built-ref> <k8s-ref>   e.g. localhost/assistant:local assistant:local" >&2; exit 2; }
[[ $# -eq 2 ]] || usage
built_ref="$1"
requested="$2"

# Force the fully-qualified form k3s resolves a bare name to. A bare name would
# be re-prefixed with localhost/ by buildah and never matched by k3s.
case "$requested" in
  */*) k8s_ref="$requested" ;;
  *)   k8s_ref="docker.io/library/${requested}" ;;
esac

command -v buildah >/dev/null 2>&1 || { echo "buildah is required" >&2; exit 1; }
command -v k3s >/dev/null 2>&1 || { echo "k3s is required" >&2; exit 1; }

tar="/tmp/$(echo "${k8s_ref}" | tr '/:' '__').tar"

echo "retag ${built_ref} -> ${k8s_ref}"
buildah tag "${built_ref}" "${k8s_ref}"

echo "export ${k8s_ref} -> ${tar}"
# OCI archives need an explicit reference in the destination. Without it,
# Buildah writes an untagged OCI index and containerd cannot import the name
# that kubelet requests (docker.io/library/<image>:<tag>).
buildah push "${k8s_ref}" "oci-archive:${tar}:${k8s_ref}"

echo "import ${tar} into k3s containerd"
sudo k3s ctr images import "${tar}"

if ! sudo k3s ctr images ls -q | grep -Fxq "${k8s_ref}"; then
  echo "ERROR: ${k8s_ref} not present after import." >&2
  echo "Inspect: sudo k3s ctr images ls | grep -i '${k8s_ref##*/}'" >&2
  exit 1
fi
echo "OK: ${k8s_ref} is available to k3s"
