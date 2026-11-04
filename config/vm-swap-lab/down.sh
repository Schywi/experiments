#!/usr/bin/env bash
# Delete the throwaway VM and everything up.sh created for it.
#
# Keeps base.img and the ssh key by default, because re-downloading and
# re-keying on every iteration is slow and buys nothing. --purge removes those
# too, so the directory returns to just its tracked files.
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
pid_file="${script_dir}/vm.pid"

if [[ -f "${pid_file}" ]]; then
  pid="$(cat "${pid_file}")"
  if kill -0 "${pid}" 2>/dev/null; then
    echo "stopping VM pid ${pid}"
    kill "${pid}" 2>/dev/null || true
    for _ in $(seq 1 20); do
      kill -0 "${pid}" 2>/dev/null || break
      sleep 1
    done
    kill -9 "${pid}" 2>/dev/null || true
  fi
  rm -f "${pid_file}"
fi

rm -rf "${script_dir}/disk.qcow2" "${script_dir}/seed" "${script_dir}/serial.log" \
  "${script_dir}/kubeconfig" "${script_dir}/runs"

if [[ "${1:-}" == "--purge" ]]; then
  echo "purging base image and ssh key"
  rm -f "${script_dir}/base.img" "${script_dir}/base.img.part" \
    "${script_dir}/id_ed25519" "${script_dir}/id_ed25519.pub"
fi

echo "vm-swap-lab removed; host untouched"
