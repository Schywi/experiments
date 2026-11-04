#!/usr/bin/env bash
# Boot one of the three swap-lab VMs.
#
# Three VMs share this directory: each keeps its own overlay, seed, serial log and
# pid under vm<N>/, and its own ssh port (2222 + N - 1). The base cloud image and
# the ssh key are shared.
#
#   VM_NUM=1 SWAP_MODE=none ./up.sh
#   VM_NUM=2 SWAP_MODE=disk SWAP_MB=384 ./up.sh
#   VM_NUM=3 SWAP_MODE=zram ZRAM_MB=384 ZRAM_ALGO=lz4 ./up.sh
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

VM_NUM="${VM_NUM:-1}"
BASE_IMAGE_URL="${BASE_IMAGE_URL:-https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img}"
# Sizing: the smallest box that still boots Ubuntu, runs sshd, and can be pushed
# past its RAM by the workload. 512 MiB is deliberate — pressure is the test.
MEM_MB="${MEM_MB:-512}"
CPUS="${CPUS:-2}"
DISK_GB="${DISK_GB:-4}"
SSH_PORT="${SSH_PORT:-$((2222 + VM_NUM - 1))}"

SWAP_MODE="${SWAP_MODE:-none}"
ZRAM_MB="${ZRAM_MB:-384}"
ZRAM_ALGO="${ZRAM_ALGO:-lz4}"
SWAP_MB="${SWAP_MB:-384}"

case "${SWAP_MODE}" in
  none | zram | disk) ;;
  *) echo "SWAP_MODE must be none|zram|disk, got '${SWAP_MODE}'" >&2; exit 2 ;;
esac

base_image="${script_dir}/base.img"
ssh_key="${script_dir}/id_ed25519"
vm_dir="${script_dir}/vm${VM_NUM}"
overlay="${vm_dir}/disk.qcow2"
seed_dir="${vm_dir}/seed"
serial_log="${vm_dir}/serial.log"
pid_file="${vm_dir}/vm.pid"

mkdir -p "${vm_dir}"

for tool in qemu-system-x86_64 qemu-img curl ssh-keygen ssh base64; do
  command -v "${tool}" >/dev/null 2>&1 || { echo "missing tool: ${tool}" >&2; exit 1; }
done

if [[ -f "${pid_file}" ]] && kill -0 "$(cat "${pid_file}")" 2>/dev/null; then
  echo "vm${VM_NUM} already running (pid $(cat "${pid_file}")): ./down.sh first" >&2
  exit 1
fi

accel=(-accel tcg,thread=multi -cpu max)
if [[ -r /dev/kvm && -w /dev/kvm ]]; then
  accel=(-accel kvm -cpu host)
fi

if [[ ! -f "${base_image}" ]]; then
  echo "downloading base image"
  curl -fL --retry 3 -o "${base_image}.part" "${BASE_IMAGE_URL}"
  mv "${base_image}.part" "${base_image}"
fi

if [[ ! -f "${ssh_key}" ]]; then
  ssh-keygen -t ed25519 -N "" -C "swap-lab" -f "${ssh_key}" >/dev/null
fi

rm -rf "${seed_dir}"
mkdir -p "${seed_dir}"
b64="$(base64 -w0 "${script_dir}/mempressure.py")"
sed -e "s|__SSH_PUBLIC_KEY__|$(cat "${ssh_key}.pub")|" \
  -e "s|__SWAP_MODE__|${SWAP_MODE}|" \
  -e "s|__ZRAM_MB__|${ZRAM_MB}|" \
  -e "s|__ZRAM_ALGO__|${ZRAM_ALGO}|" \
  -e "s|__SWAP_MB__|${SWAP_MB}|" \
  -e "s|__MEMPRESSURE_B64__|${b64}|" \
  "${script_dir}/cloud-init.yaml" >"${seed_dir}/user-data"
printf 'instance-id: swap-lab-%s\nlocal-hostname: swap-lab-%s\n' "${VM_NUM}" "${VM_NUM}" >"${seed_dir}/meta-data"

rm -f "${overlay}"
qemu-img create -f qcow2 -b "${base_image}" -F qcow2 "${overlay}" "${DISK_GB}G" >/dev/null

echo "booting swap-lab-${VM_NUM}: ${CPUS} vcpu, ${MEM_MB} MiB, ${DISK_GB} GiB, swap=${SWAP_MODE}" \
  "(zram ${ZRAM_MB}M ${ZRAM_ALGO} / disk ${SWAP_MB}M), ssh ${SSH_PORT}"

qemu-system-x86_64 \
  -name "swap-lab-${VM_NUM}" \
  -m "${MEM_MB}" -smp "${CPUS}" \
  "${accel[@]}" \
  -drive file="${overlay}",if=virtio,format=qcow2 \
  -drive file=fat:"${seed_dir}",if=virtio,format=vvfat,label=cidata \
  -netdev user,id=net0,hostfwd=tcp:127.0.0.1:"${SSH_PORT}"-:22 \
  -device virtio-net-pci,netdev=net0 \
  -display none -serial file:"${serial_log}" \
  -pidfile "${pid_file}" -daemonize

echo "waiting for ssh"
for _ in $(seq 1 120); do
  if ssh -F /dev/null -i "${ssh_key}" -p "${SSH_PORT}" \
    -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
    -o ConnectTimeout=5 "ubuntu@127.0.0.1" true 2>/dev/null; then
    break
  fi
  sleep 5
done

echo "waiting for the swap setup to finish (cloud-init)"
for _ in $(seq 1 60); do
  if ssh -F /dev/null -i "${ssh_key}" -p "${SSH_PORT}" \
    -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
    "ubuntu@127.0.0.1" 'test -s /var/tmp/swap-mode' 2>/dev/null; then
    break
  fi
  sleep 5
done

ssh -F /dev/null -i "${ssh_key}" -p "${SSH_PORT}" \
  -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
  "ubuntu@127.0.0.1" 'cat /var/tmp/swap-mode; echo; zramctl /dev/zram0 2>/dev/null || true'
