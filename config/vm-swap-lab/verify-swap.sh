#!/usr/bin/env bash
# Report — and optionally assert — the swap state of the lab.
#
# Three readings, none of which trusts a file we wrote:
#   * /proc/swaps: the node really has a swap device;
#   * the node's /configz: the kubelet's own report of failSwapOn and
#     memorySwap.swapBehavior;
#   * the pod cgroups: memory.swap.max at pod-slice and container-scope level.
#
# The container scope is the one that matters. kubelet writes its allowance
# there, and a child cgroup cannot exceed its parent, so a limit of 0 on the
# browser container blocks swap even when the pod slice says `max`. That is why
# this script prints both levels instead of one.
#
# Usage: ./verify-swap.sh [expected-container-limit]
#   no argument  → report only, exit 0
#   0 | max | N  → exit 1 unless every browser container (a scope with a memory
#                  limit set) carries exactly that memory.swap.max
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "${script_dir}/lib.sh"

expected="${1:-}"
namespace="${NAMESPACE:-sandbox-demo}"
json="${script_dir}/serial.log.configz.json"

echo "== swap backing =="
vm_ssh 'cat /var/tmp/swap-mode 2>/dev/null || echo "swap: (not recorded)"; echo; swapon --show 2>/dev/null || true; echo; cat /proc/swaps; if command -v zramctl >/dev/null && [ -e /dev/zram0 ]; then echo; zramctl /dev/zram0; awk "{print \"  achieved ratio: \" \$2/\$3 \" : 1 (data/compr)\"}" <(zramctl --noheadings --bytes --output DATA,COMPR /dev/zram0) 2>/dev/null || true; fi'

echo
echo "== kubelet running config (/configz) =="
node="$(vm_ssh "sudo k3s kubectl get nodes -o jsonpath='{.items[0].metadata.name}'")"
vm_ssh "sudo k3s kubectl get --raw /api/v1/nodes/${node}/proxy/configz" >"${json}"
read -r fail_swap_on swap_behavior < <(python3 - "${json}" <<'PY'
import json, sys
cfg = json.load(open(sys.argv[1]))["kubeletconfig"]
swap = cfg.get("memorySwap") or {}
print(cfg.get("failSwapOn"), swap.get("swapBehavior", "unset"))
PY
)
echo "failSwapOn=${fail_swap_on} memorySwap.swapBehavior=${swap_behavior}"

echo
echo "== pod cgroups (browser containers are the scopes with a memory limit) =="
vm_ssh "sudo sh -s ${namespace} ${expected:-report}" <<'SH'
namespace="$1"
expected="$2"

saw_browser=0
mismatch=0
for uid in $(k3s kubectl -n "${namespace}" get pods -o jsonpath='{range .items[*]}{.metadata.uid}{"\n"}{end}'); do
  underscored="$(printf '%s' "${uid}" | tr '-' '_')"
  for slice in /sys/fs/cgroup/kubepods.slice/kubepods-*.slice/kubepods-*-pod"${underscored}".slice; do
    [ -d "${slice}" ] || continue
    printf '  %s pod swap.max=%s\n' "${slice##*/}" "$(cat "${slice}/memory.swap.max" 2>/dev/null)"
    for scope in "${slice}"/*.scope; do
      [ -d "${scope}" ] || continue
      mem="$(cat "${scope}/memory.max" 2>/dev/null || echo max)"
      swap="$(cat "${scope}/memory.swap.max" 2>/dev/null || echo n/a)"
      printf '    container mem.max=%s swap.max=%s\n' "${mem}" "${swap}"
      [ "${mem}" = "max" ] && continue
      saw_browser=$((saw_browser + 1))
      if [ "${expected}" = "nonzero" ]; then
        if [ "${swap}" = "0" ]; then
          printf '    MISMATCH: expected a non-zero swap allowance, found 0\n'
          mismatch=$((mismatch + 1))
        fi
      elif [ "${expected}" != "report" ] && [ "${swap}" != "${expected}" ]; then
        printf '    MISMATCH: expected swap.max=%s\n' "${expected}"
        mismatch=$((mismatch + 1))
      fi
    done
  done
done

if [ "${saw_browser}" -eq 0 ]; then
  echo "  (no pod with a memory limit is running yet — start the pool first)" >&2
fi
if [ "${mismatch}" -gt 0 ]; then
  echo "FAIL: ${mismatch} browser container(s) do not match the expected swap allowance (${expected})" >&2
  exit 1
fi
if [ "${expected}" != "report" ] && [ "${saw_browser}" -gt 0 ]; then
  echo "OK: ${saw_browser} browser container(s) match the expected swap allowance (${expected})"
fi
SH

echo
if [[ "${fail_swap_on}" != "False" && "${fail_swap_on}" != "false" ]]; then
  echo "FAIL: kubelet reports failSwapOn=${fail_swap_on}; the drop-in was not applied" >&2
  exit 1
fi
if [[ "${swap_behavior}" != "LimitedSwap" ]]; then
  echo "WARNING: memorySwap.swapBehavior=${swap_behavior}; this lab runs LimitedSwap" >&2
fi
