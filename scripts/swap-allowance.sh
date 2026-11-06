#!/usr/bin/env bash
# Give the sandbox pods of one memory tier a per-pod swap allowance.
#
# Kubernetes has no field for this. kubelet computes one number for every
# Burstable pod (LimitedSwap: request / node allocatable * node swap, ~66 MiB
# here) and writes it on the container scope; a class that needs a different
# number gets it written directly. This script is that write, on the host, and it
# selects pods by the annotation the chart puts on their template:
#
#   session-lab.io/spill = none | zram-2to1 | zram-5to1 | disk
#
# It writes both the pod slice and every container scope under it, because a
# child cgroup cannot exceed its parent: writing only the slice leaves kubelet's
# container-level 0 in force. Values are read back, so a call cannot report
# success unless the kernel accepted the number.
#
#   sudo scripts/swap-allowance.sh --dry-run             # who would be written
#   sudo scripts/swap-allowance.sh --verify              # what is set right now
#   sudo scripts/swap-allowance.sh --bytes 268435456     # 256 MiB per pod
#   sudo scripts/swap-allowance.sh --tier zram-2to1       # only that tier
#   sudo scripts/swap-allowance.sh --revoke              # back to 0
#
# Requires root: /sys/fs/cgroup is root-owned. It does not restart anything, does
# not touch the kubelet config, and is reversible with --revoke.
set -euo pipefail

namespace="${NAMESPACE:-sandbox-demo}"
annotation="session-lab.io/spill"
bytes="${SWAP_ALLOWANCE:-268435456}"   # 256 MiB: the swap study's per-pod arm
tier=""
mode="write"

usage() {
  sed -n '2,26p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
  exit "${1:-0}"
}

while (($#)); do
  case "$1" in
    --dry-run) mode="dry-run" ;;
    --verify) mode="verify" ;;
    --revoke) mode="revoke" ;;
    --bytes) bytes="${2:?--bytes needs a value}"; shift ;;
    --tier) tier="${2:?--tier needs a value}"; shift ;;
    --namespace) namespace="${2:?--namespace needs a value}"; shift ;;
    --help | -h) usage 0 ;;
    *) echo "unknown argument: $1" >&2; usage 2 ;;
  esac
  shift
done

case "${bytes}" in
  '' | *[!0-9]*) echo "--bytes wants a number of bytes, got '${bytes}'" >&2; exit 2 ;;
esac

kubectl_bin="kubectl"
command -v kubectl >/dev/null 2>&1 || kubectl_bin="k3s kubectl"

if [[ "${mode}" != "verify" && "${mode}" != "dry-run" && "${EUID}" -ne 0 ]]; then
  echo "writing memory.swap.max needs root: run with sudo (or pass --dry-run)" >&2
  exit 1
fi

# pod_uid prints the UID of every pod carrying a spill tier, one per line, with
# its tier: "<uid> <tier>". A pod with no annotation is not a candidate: nothing
# declared it swappable.
pods() {
  ${kubectl_bin} -n "${namespace}" get pods \
    -o jsonpath='{range .items[*]}{.metadata.uid}{" "}{.metadata.annotations.session-lab\.io/spill}{"\n"}{end}' |
    awk '$2 != "" && $2 != "none" {print $1, $2}'
}

# cgroup_dirs prints every cgroup directory that carries this pod's slices: the
# pod slice itself, then one per container scope.
cgroup_dirs() {
  local uid="$1" underscored slice
  underscored="$(printf '%s' "${uid}" | tr '-' '_')"
  for slice in /sys/fs/cgroup/kubepods.slice/kubepods-*.slice/kubepods-*-pod"${underscored}".slice; do
    [[ -d "${slice}" ]] || continue
    printf '%s\n' "${slice}"
    for scope in "${slice}"/*.scope; do
      [[ -d "${scope}" ]] && printf '%s\n' "${scope}"
    done
  done
}

read_back() {
  local file="$1"
  [[ -r "${file}" ]] || return 1
  cat "${file}"
}

written=0
skipped=0
while read -r uid pod_tier; do
  [[ -n "${uid}" ]] || continue
  if [[ -n "${tier}" && "${pod_tier}" != "${tier}" ]]; then
    skipped=$((skipped + 1))
    continue
  fi
  dirs="$(cgroup_dirs "${uid}")"
  if [[ -z "${dirs}" ]]; then
    echo "pod ${uid} (${pod_tier}): no cgroup found; skipped" >&2
    skipped=$((skipped + 1))
    continue
  fi

  target="${bytes}"
  [[ "${mode}" == "revoke" ]] && target="0"

  while read -r dir; do
    file="${dir}/memory.swap.max"
    [[ -e "${file}" ]] || continue
    case "${mode}" in
      verify)
        printf '%s %s\n' "$(read_back "${file}")" "${file}"
        continue
        ;;
      dry-run)
        printf 'would write %s to %s\n' "${target}" "${file}"
        continue
        ;;
    esac
    printf '%s' "${target}" >"${file}"
    got="$(read_back "${file}")"
    if [[ "${got}" != "${target}" ]]; then
      echo "kernel kept ${got} instead of ${target} for ${file}" >&2
      exit 1
    fi
  done <<<"${dirs}"
  written=$((written + 1))
  case "${mode}" in
    dry-run | verify) ;;
    *) echo "pod ${uid} (${pod_tier}): memory.swap.max=${target}" ;;
  esac
done < <(pods)

case "${mode}" in
  verify) echo "-- verify lists every cgroup value; 0 means the pod cannot swap" ;;
  dry-run) echo "-- dry run: ${written} pod(s) would be written, ${skipped} skipped" ;;
  *) echo "-- ${written} pod(s) written, ${skipped} skipped, ${bytes} bytes per cgroup in ${namespace}" ;;
esac
