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

# Guest facts first. Every regression in this lab so far was a wrong guest
# assumption (Ubuntu image reused, zram module missing, user in a group Fedora
# does not have), and each one only surfaced minutes later as "pool did not fill"
# or a table full of failures. Printing the distro, kernel and zram capabilities
# here means the first verification after boot answers "is this the machine I
# think it is" in eight seconds.
echo "== guest =="
vm_ssh '. /etc/os-release 2>/dev/null && echo "distro: ${PRETTY_NAME:-unknown}"
        echo "kernel: $(uname -r)"
        if [ -e /sys/block/zram0/recomp_algorithm ]; then
          echo "multi-comp: yes  (recomp_algorithm present)"
        else
          echo "multi-comp: NO   (no recomp_algorithm: CONFIG_ZRAM_MULTI_COMP is off)"
        fi
        [ -e /sys/block/zram0/comp_algorithm ] && echo "primary algorithms: $(cat /sys/block/zram0/comp_algorithm)"
        if [ -e /sys/block/zram0/recomp_algorithm ]; then
          echo "secondary algorithms: '$(cat /sys/block/zram0/recomp_algorithm)' (the active one is in brackets)"
        fi'

# REQUIRE_MULTICOMP=1 makes a recompression arm fail here, at boot, instead of
# after install + kubelet + a ladder that would only prove the arm duplicates the
# control.
if [[ "${REQUIRE_MULTICOMP:-0}" == "1" ]]; then
  if ! vm_ssh 'test -e /sys/block/zram0/recomp_algorithm'; then
    echo >&2
    echo "FAIL: this arm needs zram recompression but the guest has no" >&2
    echo "      /sys/block/zram0/recomp_algorithm (CONFIG_ZRAM_MULTI_COMP is off)." >&2
    echo "      Ubuntu ships it off in every release checked (noble 6.8, plucky 6.14:" >&2
    echo "      amd64 'n'). Fedora 44 builds it in ('=y'). Fix the image, not the arm." >&2
    exit 1
  fi
fi

echo
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
