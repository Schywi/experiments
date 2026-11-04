#!/usr/bin/env bash
# Put the guest's kubelet into LimitedSwap and prove it took effect.
#
# This has to happen before any measurement: kubelet inherits whatever drop-in is
# on disk at start-up, and an earlier generation of runs measured three "healthy"
# arms that were all still NoSwap — memory.swap.max was never set, so per-pod
# swap could not be exercised at all.
#
# Exit status is the verdict: 0 only when /configz reports LimitedSwap.
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "${script_dir}/lib.sh"

echo "writing the kubelet drop-in and restarting k3s"
vm_ssh "sudo tee /var/lib/rancher/k3s/agent/etc/kubelet.conf.d/90-swap.conf >/dev/null <<'EOF'
apiVersion: kubelet.config.k8s.io/v1beta1
kind: KubeletConfiguration
failSwapOn: false
memorySwap:
  swapBehavior: LimitedSwap
EOF"

# Do NOT trust this exit status. systemctl restart blocks until the start job
# finishes, and k3s re-imports the airgap bundles on every start — with the
# 332 MB browser image that takes longer than systemd's window, so the command
# returns non-zero while k3s comes up perfectly well a few seconds later
# (observed: rc=1 after 10 s, node Ready ~40 s after that). The node becoming
# Ready is the real signal.
vm_ssh 'sudo systemctl restart k3s' || echo "  restart returned non-zero; waiting for the node anyway"
if ! wait_node "${WAIT_NODE_TRIES:-120}"; then
  echo "FAIL: the node never became Ready after the kubelet restart" >&2
  exit 1
fi

behavior="$(vm_ssh 'sudo k3s kubectl get --raw /api/v1/nodes/$(sudo k3s kubectl get nodes -o jsonpath="{.items[0].metadata.name}")/proxy/configz' 2>/dev/null |
  tr ',' '\n' | sed -n 's/.*"swapBehavior":"\([A-Za-z]*\)".*/\1/p' | head -1)"

echo "kubelet reports memorySwap.swapBehavior=${behavior:-unknown}"
if [[ "${behavior}" != "LimitedSwap" ]]; then
  echo "FAIL: kubelet did not pick up LimitedSwap; nothing can be measured" >&2
  exit 1
fi
