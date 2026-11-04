#!/usr/bin/env bash
# Cache the agent-sandbox release manifest ONCE, on the host, like fetch-k3s.sh
# does for the k3s artifacts.
#
# Why this exists: install-sandbox.sh used to `curl` this manifest from github.com
# on every run, while every other artifact (k3s binary, airgap images, browser
# image) was already cached. On 2026-11-04 the host resolver dropped github.com for
# about 90 seconds — `curl (6) Could not resolve host` — and both arms of a
# 30-minute play died two minutes in, at the same step, with nothing measured. The
# network was up minutes earlier (fetch-k3s resolved v1.37.2 at 14:05) and the
# play still reported `failed=0`, so the only visible symptom was an empty table.
#
# With the manifest cached, a play needs no egress at all.
#
# Fetched (into cache/):
#   sandbox-<version>.yaml   the agent-sandbox release manifest, digest-verified
#
# Usage: ./fetch-sandbox.sh [version]
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
cache_dir="${script_dir}/cache"
mkdir -p "${cache_dir}"

SANDBOX_VERSION="${1:-${SANDBOX_VERSION:-v1.0.7}}"
# Same digest config/sandbox/install.sh records. Bump both together, or the
# verification below will refuse the new manifest — which is the point.
EXPECTED_SHA256="${EXPECTED_SHA256:-136c10c9347cb5a6740b351444cae82f404e883c3f520272257ba785f2af0da8}"
dest="${cache_dir}/sandbox-${SANDBOX_VERSION}.yaml"
url="https://github.com/kubernetes-sigs/agent-sandbox/releases/download/${SANDBOX_VERSION}/sandbox-with-extensions.yaml"

echo "agent-sandbox ${SANDBOX_VERSION}"

verify() {
  local got
  got="$(sha256sum "$1" | awk '{print $1}')"
  if [[ "${got}" != "${EXPECTED_SHA256}" ]]; then
    echo "sha256 mismatch: expected ${EXPECTED_SHA256}, got ${got}" >&2
    return 1
  fi
  echo "  digest ok (${got})"
}

if [[ -s "${dest}" ]] && verify "${dest}" >/dev/null 2>&1; then
  echo "  have $(basename "${dest}") ($(du -h "${dest}" | cut -f1))"
  exit 0
fi

echo "  fetching $(basename "${dest}")"
# Same retry policy as fetch-k3s.sh: this is the only host-egress step left, so a
# transient resolver or TLS failure gets several chances before the play dies.
curl -fL --retry 8 --retry-delay 5 --retry-all-errors --connect-timeout 20 \
  --progress-bar -o "${dest}.part" "${url}" || {
  rm -f "${dest}.part"
  echo "could not download ${url}" >&2
  echo "if the host has no egress, copy the manifest here manually as:" >&2
  echo "  ${dest}" >&2
  exit 1
}
mv "${dest}.part" "${dest}"
verify "${dest}"
