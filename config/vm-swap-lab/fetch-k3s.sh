#!/usr/bin/env bash
# Fetch the k3s artifacts ONCE, on the host, so every VM boots without egress.
#
# Why: a cloud-init `curl -sfL https://get.k3s.io | sh` inside the guest failed
# with "Failed to connect to update.k3s.io port 443 after 224659 ms", so k3s was
# never installed and up.sh reported "node did not become Ready within 300s" —
# a network fault disguised as a timeout. With the artifacts cached, provisioning
# stops depending on the internet entirely.
#
# Fetched (into cache/):
#   k3s                              the server binary  (~70 MB)
#   k3s-airgap-images-amd64.tar.zst  the images k3s needs (~250 MB)
#   install.sh                       get.k3s.io's installer, run with SKIP_DOWNLOAD
#
# Usage: ./fetch-k3s.sh [version]
#   version defaults to the version on the stable channel.
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
cache_dir="${script_dir}/cache"
mkdir -p "${cache_dir}"

version="${1:-}"
if [[ -z "${version}" ]]; then
  # update.k3s.io is the host the guest's installer timed out on (224 s, no
  # answer), and it is unreachable from here too. GitHub answers, so resolve the
  # release there and only fall back to the channel API.
  echo "resolving the latest k3s release"
  version="$(curl -fsSL --retry 3 --retry-all-errors --connect-timeout 15 \
    https://api.github.com/repos/k3s-io/k3s/releases/latest 2>/dev/null |
    sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)"
  if [[ -z "${version}" ]]; then
    echo "  github api did not answer; trying the channel API"
    version="$(curl -fsSL --retry 2 --retry-all-errors --connect-timeout 10 \
      https://update.k3s.io/v1-release/channels/stable 2>/dev/null |
      tr ',' '\n' | sed -n 's/.*"latest":"\([^"]*\)".*/\1/p' | head -1)"
  fi
  [[ -n "${version}" ]] || {
    echo "could not resolve a k3s version; run: $0 v1.36.5+k3s1" >&2
    exit 1
  }
fi
echo "k3s ${version}"

base="https://github.com/k3s-io/k3s/releases/download/${version}"

fetch() {
  local url="$1" dest="$2"
  if [[ -s "${dest}" ]]; then
    echo "  have $(basename "${dest}") ($(du -h "${dest}" | cut -f1))"
    return 0
  fi
  echo "  fetching $(basename "${dest}")"
  curl -fL --retry 8 --retry-delay 5 --retry-all-errors --connect-timeout 20 \
    --progress-bar -o "${dest}.part" "${url}"
  mv "${dest}.part" "${dest}"
}

fetch "${base}/k3s" "${cache_dir}/k3s"
fetch "${base}/k3s-airgap-images-amd64.tar.zst" "${cache_dir}/k3s-airgap-images-amd64.tar.zst"
fetch "https://get.k3s.io" "${cache_dir}/install.sh"
chmod 0755 "${cache_dir}/k3s" "${cache_dir}/install.sh"

echo "${version}" >"${cache_dir}/VERSION"
echo
echo "cached in ${cache_dir}:"
ls -lh "${cache_dir}" | sed 's/^/  /'
echo
echo "up.sh serves this directory to the guest at http://10.0.2.2:8099"
