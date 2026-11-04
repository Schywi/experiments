#!/usr/bin/env bash
# Stop one swap-lab VM (and, with --purge, delete its overlay and seed).
#
#   VM_NUM=2 ./down.sh              # stop, keep the disk for a rerun
#   VM_NUM=2 ./down.sh --purge      # stop and delete vm2/
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
VM_NUM="${VM_NUM:-1}"
vm_dir="${script_dir}/vm${VM_NUM}"
pid_file="${vm_dir}/vm.pid"

if [[ -f "${pid_file}" ]]; then
  pid="$(cat "${pid_file}")"
  if kill -0 "${pid}" 2>/dev/null; then
    echo "stopping vm${VM_NUM} (pid ${pid})"
    kill "${pid}" 2>/dev/null || true
    for _ in $(seq 1 20); do
      kill -0 "${pid}" 2>/dev/null || break
      sleep 1
    done
    kill -9 "${pid}" 2>/dev/null || true
  fi
  rm -f "${pid_file}"
fi

if [[ "${1:-}" == "--purge" ]]; then
  echo "purging ${vm_dir}"
  rm -rf "${vm_dir}"
else
  rm -f "${vm_dir}/disk.qcow2" "${vm_dir}/serial.log"
  rm -rf "${vm_dir}/seed"
fi

echo "vm${VM_NUM} stopped"
