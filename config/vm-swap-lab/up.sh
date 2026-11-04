#!/usr/bin/env bash
# Boot the disposable k3s node for the swap A/B experiment.
#
# Everything this creates lives in this directory: the base image, a qcow2
# overlay on it, the cloud-init seed and the ssh key. `down.sh` deletes them.
# Nothing on the host is modified — no /etc/rancher, no kubelet restart, no sudo.
#
# Requires qemu (and ideally /dev/kvm). If /dev/kvm is missing the VM still
# boots with software emulation; the script says so, because every timing
# measured in that VM is then dominated by emulation and not comparable to
# anything else.
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

# Fedora Cloud Base 44, not Ubuntu: the guest must have a kernel built with
# CONFIG_ZRAM_MULTI_COMP, and Ubuntu ships it disabled in every release checked
# (noble 6.8 and plucky 6.14 annotations: amd64 'n'). Fedora 44 enables it, so
# recomp_algorithm / recompress / idle exist and the two-layer test is possible.
# Filename is pinned; bump it with the release.
BASE_IMAGE_URL="${BASE_IMAGE_URL:-https://download.fedoraproject.org/pub/fedora/linux/releases/44/Cloud/x86_64/images/Fedora-Cloud-Base-Generic-44-1.7.x86_64.qcow2}"
MEM_MB="${MEM_MB:-3072}"
# One vCPU per VM by default: the arms are meant to be a fleet of small boxes,
# not one big one. Note that at 1 vCPU the CPU wall lands at ~3-4 concurrent
# sessions (0.26 cores each), which is *below* the memory wall — so bump
# --vm-cpus in the playbook when the experiment is about memory/swap, or the
# arms will all measure the CPU wall and differ by nothing.
CPUS="${CPUS:-1}"
DISK_GB="${DISK_GB:-8}"
SSH_PORT="${SSH_PORT:-2222}"

# The experiment's axis: how memory is backed.
#   SWAP_MODE=none   — baseline, RAM only
#   SWAP_MODE=zram   — compressed RAM; ZRAM_ALGO picks the ratio/cost trade
#   SWAP_MODE=disk   — swap file on the root disk: real capacity, ms latency
# Size is explicit MiB, never ram/2: 1.5 GiB of zram on a 3 GiB node takes RAM
# away from the pods it is meant to help.
SWAP_MODE="${SWAP_MODE:-zram}"
ZRAM_MB="${ZRAM_MB:-768}"
ZRAM_ALGO="${ZRAM_ALGO:-lz4}"      # lz4 ≈2:1 · lzo-rle ≈2.5:1 · zstd ≈5:1 (measured)
SWAP_MB="${SWAP_MB:-1024}"

case "${SWAP_MODE}" in
  none | zram | disk) ;;
  *)
    echo "SWAP_MODE must be none|zram|disk, got '${SWAP_MODE}'" >&2
    exit 2
    ;;
esac
case "${ZRAM_ALGO}" in
  lz4 | lzo-rle | zstd | lz4hc | deflate) ;;
  *)
    echo "ZRAM_ALGO must be one of lz4|lzo-rle|zstd|lz4hc|deflate, got '${ZRAM_ALGO}'" >&2
    exit 2
    ;;
esac

base_image="${script_dir}/base.img"
overlay="${script_dir}/disk.qcow2"
seed_dir="${script_dir}/seed"
ssh_key="${script_dir}/id_ed25519"
serial_log="${script_dir}/serial.log"
pid_file="${script_dir}/vm.pid"

for tool in qemu-system-x86_64 qemu-img curl ssh-keygen ssh; do
  command -v "${tool}" >/dev/null 2>&1 || {
    echo "missing required tool: ${tool}" >&2
    exit 1
  }
done

if [[ -f "${pid_file}" ]] && kill -0 "$(cat "${pid_file}")" 2>/dev/null; then
  echo "a VM is already running (pid $(cat "${pid_file}")): run down.sh first" >&2
  exit 1
fi

accel=(-accel tcg,thread=multi -cpu max)
if [[ -r /dev/kvm && -w /dev/kvm ]]; then
  accel=(-accel kvm -cpu host)
else
  cat >&2 <<'EOF'
warning: /dev/kvm is not available in this shell.
         The VM will run under software emulation (tcg). It works, but boot and
         every guest operation are 5-20x slower, so latencies measured inside it
         are not comparable to the host. Launch this script from a shell that has
         /dev/kvm if the numbers matter.
EOF
fi

# The cached base image is keyed to the URL that produced it. Without this, the
# 593 MB Ubuntu image already on disk would be reused after the switch to Fedora
# and the guest would silently stay Ubuntu — the exact kernel that cannot do
# recompression.
image_stamp="${base_image}.url"
if [[ -f "${base_image}" && -f "${image_stamp}" && "$(cat "${image_stamp}")" != "${BASE_IMAGE_URL}" ]]; then
  echo "base image is from a different URL; discarding it"
  rm -f "${base_image}"
fi
if [[ ! -f "${base_image}" ]]; then
  echo "downloading ${BASE_IMAGE_URL}"
  curl -fL --retry 3 -o "${base_image}.part" "${BASE_IMAGE_URL}"
  mv "${base_image}.part" "${base_image}"
  printf '%s\n' "${BASE_IMAGE_URL}" > "${image_stamp}"
fi

if [[ ! -f "${ssh_key}" ]]; then
  echo "generating ${ssh_key}"
  ssh-keygen -t ed25519 -N "" -C "vm-swap-lab" -f "${ssh_key}" >/dev/null
fi

# The seed is a vfat directory qemu exposes with the label cloud-init's NoCloud
# datasource looks for, which avoids genisoimage/cloud-localds (neither is
# installed here). Two details are load-bearing:
#   * `label=` is an option of qemu's vvfat driver, so the driver has to be named
#     with `format=vvfat`. `format=raw` selects the raw driver, which rejects
#     `label` ("Block format 'raw' does not support the option 'label'"), and
#     leaving `format` out makes qemu probe a json wrapper and guess raw — same
#     error.
#   * the seed is read-only (`fat:` not `fat:rw:`): cloud-init only reads it, and
#     a read-only vvfat needs no write target, so no temporary qcow file.
rm -rf "${seed_dir}"
mkdir -p "${seed_dir}"

# Serve the cached k3s artifacts to the guest at 10.0.2.2 (QEMU user-net's alias
# for the host). Started only when the cache exists; cloud-init falls back to the
# network when the base is empty, so a fresh checkout still boots (slowly).
k3s_base_url=""
cache_dir="${script_dir}/cache"
http_pid=""
if [[ -s "${cache_dir}/k3s" && -s "${cache_dir}/install.sh" ]]; then
  http_port="${HTTP_PORT:-8099}"
  ( cd "${cache_dir}" && python3 -m http.server "${http_port}" --bind 0.0.0.0 >/dev/null 2>&1 ) &
  http_pid=$!
  k3s_base_url="http://10.0.2.2:${http_port}"
  # Prove it answers before booting. If the port is still held by an earlier run
  # the server dies instantly, the guest cannot download, and the failure shows up
  # 15 minutes later as "node did not become Ready" — the misleading message this
  # whole airgap change exists to remove.
  served=0
  for _ in $(seq 1 20); do
    if curl -fsS --max-time 1 -o /dev/null "http://127.0.0.1:${http_port}/install.sh" 2>/dev/null; then
      served=1
      break
    fi
    sleep 0.5
  done
  if [[ "${served}" -ne 1 ]]; then
    echo "artifact server did not answer on 127.0.0.1:${http_port} (port busy?)" >&2
    exit 1
  fi
  echo "serving k3s artifacts from ${cache_dir} at ${k3s_base_url}"
else
  echo "warning: ${cache_dir} is empty — run ./fetch-k3s.sh for an offline boot" >&2
fi
cleanup_http() { [[ -n "${http_pid}" ]] && kill "${http_pid}" 2>/dev/null || true; }
trap cleanup_http EXIT

sed -e "s|__SSH_PUBLIC_KEY__|$(cat "${ssh_key}.pub")|" \
  -e "s|__SWAP_MODE__|${SWAP_MODE}|" \
  -e "s|__ZRAM_MB__|${ZRAM_MB}|" \
  -e "s|__ZRAM_ALGO__|${ZRAM_ALGO}|" \
  -e "s|__SWAP_MB__|${SWAP_MB}|" \
  -e "s|__K3S_BASE_URL__|${k3s_base_url}|" \
  "${script_dir}/cloud-init.yaml" >"${seed_dir}/user-data"
cp "${script_dir}/meta-data.yaml" "${seed_dir}/meta-data"

rm -f "${overlay}"
qemu-img create -f qcow2 -b "${base_image}" -F qcow2 "${overlay}" "${DISK_GB}G" >/dev/null

echo "booting vm-swap-lab (${CPUS} vcpu, ${MEM_MB} MiB, ${DISK_GB} GiB overlay) on ssh port ${SSH_PORT}"
case "${SWAP_MODE}" in
  zram) echo "  swap: zram ${ZRAM_MB} MiB, algorithm ${ZRAM_ALGO}" ;;
  disk) echo "  swap: disk swapfile ${SWAP_MB} MiB, no zram" ;;
  none) echo "  swap: none (RAM only)" ;;
esac
qemu-system-x86_64 \
  -name vm-swap-lab \
  -m "${MEM_MB}" -smp "${CPUS}" \
  "${accel[@]}" \
  -drive file="${overlay}",if=virtio,format=qcow2 \
  -drive file=fat:"${seed_dir}",if=virtio,format=vvfat,label=cidata \
  -netdev user,id=net0,hostfwd=tcp:127.0.0.1:"${SSH_PORT}"-:22 \
  -device virtio-net-pci,netdev=net0 \
  -display none -serial file:"${serial_log}" \
  -pidfile "${pid_file}" -daemonize

echo "waiting for ssh (serial log: ${serial_log})"
for _ in $(seq 1 120); do
  if ssh -i "${ssh_key}" -p "${SSH_PORT}" -o StrictHostKeyChecking=no \
    -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -o ConnectTimeout=5 \
    "${SSH_USER}@127.0.0.1" true 2>/dev/null; then
    break
  fi
  sleep 5
done

echo "waiting for k3s"
# shellcheck source=lib.sh
source "${script_dir}/lib.sh"
# 180 × 5 s = 15 min. The guest has to import the airgap image bundle (~250 MB)
# before the node registers; 5 minutes was only ever enough with network installs.
wait_node "${WAIT_NODE_TRIES:-180}"
# The artifacts have been copied by now; stop serving them (the EXIT trap covers
# the failure path).
cleanup_http

echo
echo "node:"
vm_ssh 'sudo k3s kubectl get nodes -o wide'
echo
echo "next: ./verify-swap.sh, then ./install-sandbox.sh, then ./run-ab.sh"
