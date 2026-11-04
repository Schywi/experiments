#!/usr/bin/env bash
# Fetch the browser image ONCE, on the host, so the guest never needs egress.
#
# Airgapping k3s was not enough: the arms then failed with
#   ErrImagePull / ImagePullBackOff
#   pulling image "us-central1-docker.pkg.dev/.../chrome-sandbox:latest-main"
# because the workload image still had to come from the network.
#
# The tar is dropped next to k3s's own airgap bundle in the guest, at
# /var/lib/rancher/k3s/agent/images/, which k3s imports at start-up — so no
# `ctr images import` step and no ordering problem.
#
# Uses skopeo when present (no daemon), otherwise docker save.
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
cache_dir="${script_dir}/cache"
mkdir -p "${cache_dir}"

image="${IMAGE:-us-central1-docker.pkg.dev/k8s-staging-images/agent-sandbox/chrome-sandbox:latest-main}"
dest="${cache_dir}/chrome-sandbox.tar"

if [[ -s "${dest}" ]]; then
  echo "have $(basename "${dest}") ($(du -h "${dest}" | cut -f1))"
  exit 0
fi

echo "fetching ${image}"
# Docker first: the daemon does the unpacking in its own root, so a shell with a
# read-only /var/tmp can still fetch. skopeo was tried first and failed with
# "creating temporary on-disk layer: open /var/tmp/...: read-only file system" —
# it writes there regardless of TMPDIR, which is why it is now the fallback.
if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
  docker pull "${image}" >/dev/null
  docker save -o "${dest}" "${image}"
elif command -v skopeo >/dev/null 2>&1; then
  tmp="${TMPDIR:-/var/tmp}"
  [[ -w "${tmp}" ]] || { tmp="${cache_dir}/tmp"; mkdir -p "${tmp}"; }
  TMPDIR="${tmp}" skopeo copy --quiet --override-os linux --override-arch amd64 \
    "docker://${image}" "docker-archive:${dest}:${image}"
else
  echo "need docker or skopeo to fetch ${image}" >&2
  exit 1
fi

ls -lh "${dest}"
echo "up.sh serves this tar to the guest; k3s imports it at start-up"
