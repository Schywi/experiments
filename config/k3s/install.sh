#!/usr/bin/env bash

set -euo pipefail

if [[ "${EUID}" -ne 0 ]]; then
  echo "run this script as root (for example: sudo $0)" >&2
  exit 1
fi

command -v curl >/dev/null 2>&1 || {
  echo "curl is required" >&2
  exit 1
}

# Keep the defaults explicit so a fresh node cannot accidentally bring up a
# second CNI or an unmanaged ingress/load-balancer implementation.
K3S_CHANNEL="${K3S_CHANNEL:-stable}"
K3S_VERSION="${K3S_VERSION:-}"
K3S_EXTRA_ARGS="${K3S_EXTRA_ARGS:-}"

export INSTALL_K3S_CHANNEL="${K3S_CHANNEL}"
export INSTALL_K3S_EXEC="server --flannel-backend=none --disable-network-policy --disable-kube-proxy --disable=traefik --disable=servicelb${K3S_EXTRA_ARGS:+ ${K3S_EXTRA_ARGS}}"

if [[ -n "${K3S_VERSION}" ]]; then
  export INSTALL_K3S_VERSION="${K3S_VERSION}"
fi

curl --fail --silent --show-error --location https://get.k3s.io | sh -

echo "k3s installed without a bundled CNI; install Cilium with config/k3s/install-cilium.sh"
