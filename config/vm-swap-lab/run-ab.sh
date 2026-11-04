#!/usr/bin/env bash
# Run the same Chromium workload twice: once with the sandbox pods forbidden to
# swap, once with the node's zram open to them.
#
# One axis, and it is a cgroup value: memory.swap.max. The kubelet setting is
# LimitedSwap in both runs, because restarting k3s between runs kills the
# sandbox pods (restartPolicy: Never) and, measured on this VM, LimitedSwap
# alone does not decide per-pod swap anyway:
#
#   * the node reports no swap capacity, so kubelet's computed allowance for the
#     browser container is 0 bytes — at the container scope, not the pod slice;
#   * with NoSwap, kubelet writes no limit at all and the pod slice keeps
#     memory.swap.max=max, i.e. unconstrained rather than forbidden.
#
# verify-swap.sh prints all of that. swap-limit.sh is what actually decides.
#
# session-lab runs *inside* the VM on purpose: internal/measure reads
# /sys/fs/cgroup on the machine it runs on, so a host-side run would sample
# nothing and every memory and swap number would be zero.
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "${script_dir}/../.." && pwd)"
# shellcheck source=lib.sh
source "${script_dir}/lib.sh"

DURATION="${DURATION:-10m}"
RATE="${RATE:-0.05}"
HOLD="${HOLD:-30s}"
REPLICAS="${REPLICAS:-4}"
SAMPLE_SECONDS="${SAMPLE_SECONDS:-5}"
# What run B may swap, per pod. Explicit bytes by default, because kubelet's own
# allowance is not stable across box sizes: 133.9 MiB on the 4 GiB VM, 0 on the
# 2 GiB one for the same template. An experiment variable has to be a number we
# set, so 256 MiB = 1 GiB of zram across 4 pods.
#   kubelet — leave Kubernetes' number in place (only useful for comparison)
#   max     — the whole zram
SWAP_ALLOWANCE="${SWAP_ALLOWANCE:-268435456}"
out_dir="${script_dir}/runs"
namespace="sandbox-demo"
pool="browser-pool"

mkdir -p "${out_dir}"

echo "== kubelet: LimitedSwap for both runs =="
vm_ssh "sudo tee /var/lib/rancher/k3s/agent/etc/kubelet.conf.d/90-swap.conf >/dev/null <<'EOF'
apiVersion: kubelet.config.k8s.io/v1beta1
kind: KubeletConfiguration
failSwapOn: false
memorySwap:
  swapBehavior: LimitedSwap
EOF"
# Checking /readyz is not enough: the API answers fine while a kubelet that
# started before the drop-in keeps the old behaviour. Compare the drop-in with
# what the kubelet reports about itself, and restart only on a real mismatch.
# (Observed: the file said LimitedSwap while /configz still said NoSwap.)
running_behavior() {
  vm_ssh 'sudo k3s kubectl get --raw /api/v1/nodes/$(sudo k3s kubectl get nodes -o jsonpath="{.items[0].metadata.name}")/proxy/configz' 2>/dev/null |
    tr ',' '\n' | sed -n 's/.*"swapBehavior":"\([A-Za-z]*\)".*/\1/p' | head -1
}
behavior="$(running_behavior || true)"
if [[ "${behavior}" != "LimitedSwap" ]]; then
  echo "  running kubelet reports memorySwap.swapBehavior=${behavior:-unknown}; restarting k3s"
  vm_ssh 'sudo systemctl restart k3s'
  wait_node 60
  behavior="$(running_behavior || true)"
fi
if [[ "${behavior}" != "LimitedSwap" ]]; then
  echo "kubelet did not pick up LimitedSwap (reports '${behavior:-unknown}'): fix that before measuring" >&2
  exit 1
fi
echo "  kubelet reports memorySwap.swapBehavior=${behavior}"

# session-lab reads the node kubeconfig and the pod cgroups itself. sudo k3s
# kubectl covers the API for the other scripts, but a run must also be able to
# read /sys/fs/cgroup as the unprivileged user, so both are checked here rather
# than discovered as a table full of zeroes after the run.
echo "== guest access for session-lab =="
vm_ssh 'mkdir -p /home/ubuntu/.kube && sudo cp /etc/rancher/k3s/k3s.yaml /home/ubuntu/.kube/config \
  && sudo chown ubuntu:ubuntu /home/ubuntu/.kube/config && chmod 600 /home/ubuntu/.kube/config'

echo "== warm pool =="
set_pool_replicas() {
  local n="$1"
  vm_kubectl "patch sandboxwarmpool ${pool} -n ${namespace} --type=merge -p '{\"spec\":{\"replicas\":${n}}}'"
}

wait_pool() {
  local want="$1"
  for _ in $(seq 1 90); do
    ready="$(vm_ssh "sudo k3s kubectl -n ${namespace} get sandboxwarmpool ${pool} -o jsonpath='{.status.readyReplicas}'" || true)"
    echo "  pool ready ${ready:-0}/${want}"
    [[ "${ready:-0}" -eq "${want}" ]] && return 0
    sleep 5
  done
  echo "pool did not reach ${want} replicas" >&2
  return 1
}

# recycle_pool replaces the warm pods. Necessary between arms: a cgroup limit
# belongs to the pod, so a pod that run A wrote memory.swap.max=0 into keeps that
# 0 forever — "leave kubelet's value alone" only means anything on pods kubelet
# created after its setting was changed.
#
# A fresh pod starts with kubelet's own allowance and only gets the arm's value
# when this script writes it, which is exactly how an earlier arm recorded 25 MiB
# of swap while claiming to be a no-swap arm. So the limit is enforced while the
# pool refills, on a short loop, and the pod slice is written before its
# containers start: a child cgroup can never exceed its parent, so kubelet's
# later write of a bigger number is harmless.
recycle_pool() {
  local value="$1" loop_pid=""

  set_pool_replicas 0
  for _ in $(seq 1 60); do
    ready="$(vm_ssh "sudo k3s kubectl -n ${namespace} get sandboxwarmpool ${pool} -o jsonpath='{.status.readyReplicas}'" || true)"
    [[ "${ready:-0}" -eq 0 ]] && break
    sleep 2
  done

  if [[ "${value}" != "kubelet" ]]; then
    (
      while sleep 0.2; do
        "${script_dir}/swap-limit.sh" "${value}" >/dev/null 2>&1 || true
      done
    ) &
    loop_pid=$!
  fi

  set_pool_replicas "${REPLICAS}"
  wait_pool "${REPLICAS}" || {
    [[ -n "${loop_pid}" ]] && kill -TERM "${loop_pid}" 2>/dev/null
    return 1
  }

  if [[ -n "${loop_pid}" ]]; then
    kill -TERM "${loop_pid}" 2>/dev/null || true
    wait "${loop_pid}" 2>/dev/null || true
  fi
}

recycle_pool 0

echo "== guest user can read pod cgroups =="
if ! vm_ssh 'for d in /sys/fs/cgroup/kubepods.slice/kubepods-burstable.slice/*.slice; do
      [ -d "$d" ] || continue
      cat "$d/memory.current" >/dev/null && exit 0
    done; exit 1'; then
  echo "cannot read /sys/fs/cgroup pod slices as ${SSH_USER}: the run would record no samples" >&2
  exit 1
fi

echo "== building session-lab for the guest =="
(
  cd "${repo_root}/apps/session-lab"
  GOOS=linux GOARCH=amd64 go build -o "${out_dir}/session-lab" ./cmd/session-lab
)
# Copy beside the target and rename into place. Renaming replaces the *name*, not
# the inode, so it works even while another session-lab (a run someone left
# going) is executing the old binary — scp straight onto it fails with
# "Text file busy".
scp -F /dev/null -i "${SSH_KEY}" -P "${SSH_PORT}" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
  "${out_dir}/session-lab" "${SSH_USER}@${VM_HOST}:/home/ubuntu/session-lab.new"
vm_ssh 'mv -f /home/ubuntu/session-lab.new /home/ubuntu/session-lab && chmod 0755 /home/ubuntu/session-lab'

# Run ids carry a timestamp: two invocations, or a run left over from earlier,
# must never share a SQLite file. Nothing is deleted in the guest — a run that
# someone else started keeps its rows.
stamp="$(date -u +%Y%m%dT%H%M%SZ)"

# A pod created while a run is in flight starts from kubelet's own value, so the
# class boundary is re-asserted during the run. Without this, only the sessions
# that happened to start on the pre-filled pods would be in the right class.
reapply_loop() {
  local value="$1"
  while sleep 5; do
    "${script_dir}/swap-limit.sh" "${value}" >/dev/null 2>&1 || true
  done
}

run_one() {
  local run_id="$1" swap_value="$2" expect="$3"
  echo
  echo "=== run ${run_id}: memory.swap.max=${swap_value} ==="
  # Fresh pods for every arm, with this arm's swap limit enforced while they are
  # created, so the first seconds of a pod are already in its class.
  recycle_pool "${swap_value}"
  "${script_dir}/swap-limit.sh" "${swap_value}"
  "${script_dir}/verify-swap.sh" "${expect}"

  reapply_loop "${swap_value}" &
  local loop_pid=$!
  trap 'kill -TERM "${loop_pid}" 2>/dev/null || true' EXIT

  vm_ssh "mkdir -p /home/ubuntu/runs"
  vm_ssh "cd /home/ubuntu && ./session-lab \
    --namespace ${namespace} --pool ${pool} \
    --policy destroy \
    --duration ${DURATION} --rate ${RATE} --hold ${HOLD} \
    --pool-replicas ${REPLICAS} --sample-seconds ${SAMPLE_SECONDS} \
    --kubeconfig /home/ubuntu/.kube/config \
    --out /home/ubuntu/runs --run-id ${run_id}"

  kill -TERM "${loop_pid}" 2>/dev/null || true
  wait "${loop_pid}" 2>/dev/null || true
  trap - EXIT

  scp -F /dev/null -i "${SSH_KEY}" -P "${SSH_PORT}" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
    "${SSH_USER}@${VM_HOST}:/home/ubuntu/runs/${run_id}.json" "${out_dir}/${run_id}.json"
  scp -F /dev/null -i "${SSH_KEY}" -P "${SSH_PORT}" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
    "${SSH_USER}@${VM_HOST}:/home/ubuntu/runs/${run_id}.db" "${out_dir}/${run_id}.db"
}

off_id="swap-off-${stamp}"
on_id="swap-on-${stamp}"
run_one "${off_id}" 0 0
if [[ "${SWAP_ALLOWANCE}" == "kubelet" ]]; then
  run_one "${on_id}" kubelet nonzero
else
  run_one "${on_id}" "${SWAP_ALLOWANCE}" "${SWAP_ALLOWANCE}"
fi

echo
echo "both runs in ${out_dir}:"
ls -l "${out_dir}" | grep -E "swap-(off|on)-${stamp}"
echo
echo "next:"
echo "  ./compare.sh ${out_dir}/${off_id}.json ${out_dir}/${on_id}.json"
echo "next: ./compare.sh"
