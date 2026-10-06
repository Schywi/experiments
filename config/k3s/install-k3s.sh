#!/usr/bin/env bash
#
# Install k3s natively on the Fedora host with Cilium-owned networking.
# Requires root (run with sudo). The k3d profile is unaffected by this script.

set -euo pipefail

command -v curl >/dev/null 2>&1 || {
  echo "curl is required" >&2
  exit 1
}

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
config_src="${script_dir}/config.yaml"
config_dst="/etc/rancher/k3s/config.yaml"

[[ -f "${config_src}" ]] || {
  echo "k3s config not found: ${config_src}" >&2
  exit 1
}

if [[ "${EUID}" -ne 0 ]]; then
  echo "install-k3s.sh must run as root (use sudo)" >&2
  exit 1
fi

# Trust the pod and service CIDRs before pods exist, so Cilium's datapath is
# not filtered by firewalld on the host.
if command -v firewall-cmd >/dev/null 2>&1; then
  firewall-cmd --permanent --zone=trusted --add-source=10.42.0.0/16 >/dev/null
  firewall-cmd --permanent --zone=trusted --add-source=10.43.0.0/16 >/dev/null
  firewall-cmd --reload >/dev/null
  echo "firewalld: trusted pod/service CIDRs 10.42.0.0/16 and 10.43.0.0/16"
fi

# Place the server config before first start so the first boot already has
# Flannel/kube-proxy/Traefik/ServiceLB disabled.
install -d -m 0755 /etc/rancher/k3s
install -m 0644 "${config_src}" "${config_dst}"

if [[ -x /usr/local/bin/k3s ]]; then
  echo "k3s already installed; restarting to apply ${config_dst}"
  systemctl restart k3s
else
  curl -sfL https://get.k3s.io | sh -
fi
systemctl enable --now k3s

# Wait for the API to answer. The node stays NotReady until a CNI exists, so
# only liveness is checked here; install-cilium.sh supplies the CNI.
for _ in $(seq 1 60); do
  if k3s kubectl get node >/dev/null 2>&1; then
    break
  fi
  sleep 2
done

k3s kubectl get nodes -o wide

echo "Native k3s installed. The node is NotReady until Cilium is installed;"
echo "run config/k3s/install-cilium.sh next."
