#!/usr/bin/env bash
#
# Remove the native k3s installation from the host. Requires root.
# This does not touch the k3d profile or any Docker-backed cluster.

set -euo pipefail

if [[ "${EUID}" -ne 0 ]]; then
  echo "delete.sh must run as root (use sudo)" >&2
  exit 1
fi

if [[ -x /usr/local/bin/k3s-uninstall.sh ]]; then
  /usr/local/bin/k3s-uninstall.sh
  echo "Native k3s removed"
else
  echo "k3s is not installed at /usr/local/bin/k3s" >&2
  exit 1
fi
