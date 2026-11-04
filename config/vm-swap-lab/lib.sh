#!/usr/bin/env bash
# Shared helpers for the vm-swap-lab scripts. Sourced, never executed.
#
# One place for the ssh invocation so the port, key and user are not repeated in
# four scripts; the scripts themselves stay readable as the sequence they run.

set -euo pipefail

VM_DIR="${VM_DIR:-$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)}"
SSH_PORT="${SSH_PORT:-2222}"
SSH_KEY="${SSH_KEY:-${VM_DIR}/id_ed25519}"
# Fedora Cloud Base's default user (Ubuntu images use `ubuntu`). The guest image
# is Fedora because it is the distro whose kernel has CONFIG_ZRAM_MULTI_COMP=y.
SSH_USER="${SSH_USER:-fedora}"
VM_HOST="${VM_HOST:-127.0.0.1}"

vm_ssh() {
  # -F /dev/null: ignore the caller's ~/.ssh/config and /etc/ssh/ssh_config. The
  # key, port and user are all explicit below, so nothing is inherited, and a
  # host with a broken system ssh config (this sandbox has one) still works.
  ssh -F /dev/null -i "${SSH_KEY}" -p "${SSH_PORT}" \
    -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -o LogLevel=ERROR -o ConnectTimeout=10 \
    "${SSH_USER}@${VM_HOST}" "$@"
}

# vm_kubectl is the VM's own kubectl. On a k3s node that is `k3s kubectl`, which
# needs sudo to read /etc/rancher/k3s/k3s.yaml.
vm_kubectl() {
  vm_ssh "sudo k3s kubectl $*"
}

vm_ready() {
  vm_ssh 'sudo k3s kubectl get --raw /readyz' >/dev/null 2>&1
}

# wait_node waits for the node to report Ready, which is what says a k3s restart
# finished rather than merely started.
wait_node() {
  local tries="${1:-60}"
  for _ in $(seq 1 "${tries}"); do
    if vm_ssh "sudo k3s kubectl wait --for=condition=Ready node --all --timeout=5s" >/dev/null 2>&1; then
      return 0
    fi
    sleep 5
  done
  echo "node did not become Ready within $((tries * 5))s" >&2
  return 1
}
