#!/usr/bin/env bash
# Dump what the guest knows about a failed rung, before the VM is destroyed.
#
# Without this, a rung that reports "pool reached 0/3" is indistinguishable from
# a box with no capacity — the reason (image pull, scheduling, probe timeout,
# API refusal) only exists inside the guest.
#
# Usage: ./collect-diagnostics.sh <output-file> [arm-name]
set -uo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "${script_dir}/lib.sh"

out="${1:?usage: collect-diagnostics.sh <output-file> [arm]}"
arm="${2:-arm}"
namespace="${NAMESPACE:-sandbox-demo}"

{
  echo "== arm =="
  echo "${arm}"
  echo
  echo "== swap mode recorded in the guest =="
  # Absent is not "none": /var/tmp/swap-mode only exists if setup-swap.sh reached
  # its end. The run that lost all swap printed "none" and hid a script that died,
  # so the two cases are reported separately, with cloud-init's own log.
  vm_ssh 'if [ -f /var/tmp/swap-mode ]; then cat /var/tmp/swap-mode;
          else echo "MISSING: setup-swap.sh did not complete"; fi;
          echo "-- cloud-init logs present --";
          ls -1 /var/log/cloud-init* 2>/dev/null || echo "none in /var/log";
          echo "-- cloud-init output (last 25 lines) --";
          tail -25 /var/log/cloud-init-output.log 2>/dev/null || echo "no cloud-init-output.log";
          echo "-- journal: cloud-init --";
          journalctl -t cloud-init --no-pager -n 30 2>/dev/null | tail -30 || echo "no cloud-init journal entries"'
  echo
  echo "== node / resources =="
  vm_ssh 'nproc; free -m; cat /proc/swaps'
  echo
  echo "== node conditions =="
  vm_ssh 'sudo k3s kubectl get nodes -o wide' 2>&1
  echo
  echo "== pool / sandboxes / pods =="
  vm_ssh "sudo k3s kubectl -n ${namespace} get sandboxwarmpool,sandboxes,pods -o wide" 2>&1 | head -40
  echo
  echo "== pod describe (last 25 lines each) =="
  for pod in $(vm_ssh "sudo k3s kubectl -n ${namespace} get pods -o name" 2>/dev/null | head -4); do
    echo "--- ${pod} ---"
    vm_ssh "sudo k3s kubectl -n ${namespace} describe ${pod}" 2>&1 | tail -25
  done
  echo
  echo "== events =="
  vm_ssh "sudo k3s kubectl -n ${namespace} get events --sort-by=.lastTimestamp" 2>&1 | tail -25
  echo
  echo "== kubelet =="
  vm_ssh 'sudo journalctl -u k3s -n 30 --no-pager 2>&1 | grep -iE "swap|evict|oom|fail" | tail -15'
} >"${out}" 2>&1

echo "diagnostics written to ${out} ($(wc -l <"${out}") lines)"
