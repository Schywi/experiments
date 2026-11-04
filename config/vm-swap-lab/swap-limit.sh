#!/usr/bin/env bash
# Set the swap allowance of every browser sandbox pod in the VM.
#
# This is the whole per-pod swap control: `memory.swap.max` is a cgroup file, and
# no Pod field expresses it. Both the pod slice and every container scope under
# it are written, because a child cannot exceed its parent: writing only the pod
# slice would leave kubelet's own container-level value (0, observed) in force.
#
# Usage:  ./swap-limit.sh 0
#         ./swap-limit.sh max
#         ./swap-limit.sh 536870912     # bytes
# Prints the number of slices written and then reads them back, so a call can
# never report success without the kernel having accepted the value.
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "${script_dir}/lib.sh"

value="${1:-}"
case "${value}" in
  kubelet)
    # Leave kubelet's own LimitedSwap allowance in place. Measured on this VM:
    # (256Mi request / 3915Mi allocatable) * 2047Mi swap = 133.9 MiB per browser
    # container. This mode only reports, so a run can use exactly what Kubernetes
    # grants instead of a number we chose.
    echo "kubelet-managed allowance requested: no write"
    value="" ;;
  0 | max | '' )
    [[ -n "${value}" ]] || { echo "usage: $0 <0|max|bytes|kubelet>" >&2; exit 2; } ;;
  *[!0-9]*)
    echo "not a cgroup value: ${value} (use 0, max, bytes, or kubelet)" >&2
    exit 2 ;;
esac

namespace="${NAMESPACE:-sandbox-demo}"

if [[ -z "${value}" ]]; then
  echo "current values (unmodified):"
  vm_ssh "sudo sh -s ${namespace}" <<'SH'
namespace="$1"
for uid in $(k3s kubectl -n "${namespace}" get pods -o jsonpath='{range .items[*]}{.metadata.uid}{"\n"}{end}'); do
  underscored="$(printf '%s' "${uid}" | tr '-' '_')"
  for slice in /sys/fs/cgroup/kubepods.slice/kubepods-*.slice/kubepods-*-pod"${underscored}".slice; do
    [ -d "${slice}" ] || continue
    for scope in "${slice}"/*.scope; do
      [ -d "${scope}" ] || continue
      mem="$(cat "${scope}/memory.max" 2>/dev/null || echo max)"
      [ "${mem}" = "max" ] && continue
      raw="$(cat "${scope}/memory.swap.max")"
      awk -v r="${raw}" -v m="${mem}" 'BEGIN{
        if (r == "max") printf "  browser mem.max=%s swap.max=max\n", m;
        else printf "  browser mem.max=%s swap.max=%s (%.1f MiB)\n", m, r, r/1048576;
      }'
    done
  done
done
SH
  exit 0
fi

vm_ssh "sudo sh -s ${value} ${namespace}" <<'SH'
set -e
value="$1"
namespace="$2"

written=0
for uid in $(k3s kubectl -n "${namespace}" get pods -o jsonpath='{range .items[*]}{.metadata.uid}{"\n"}{end}'); do
  underscored="$(printf '%s' "${uid}" | tr '-' '_')"
  for slice in /sys/fs/cgroup/kubepods.slice/kubepods-*.slice/kubepods-*-pod"${underscored}".slice; do
    [ -d "${slice}" ] || continue
    if ! printf '%s\n' "${value}" >"${slice}/memory.swap.max"; then
      echo "FAILED to write ${slice}/memory.swap.max" >&2
      exit 1
    fi
    written=$((written + 1))
    for scope in "${slice}"/*.scope; do
      [ -d "${scope}" ] || continue
      printf '%s\n' "${value}" >"${scope}/memory.swap.max" || {
        echo "FAILED to write ${scope}/memory.swap.max" >&2
        exit 1
      }
    done
  done
done

echo "wrote memory.swap.max=${value} to ${written} pod slice(s) plus their container scopes"
SH

echo
echo "read back:"
vm_ssh "sudo sh -s ${namespace}" <<'SH'
namespace="$1"
for uid in $(k3s kubectl -n "${namespace}" get pods -o jsonpath='{range .items[*]}{.metadata.uid}{"\n"}{end}'); do
  underscored="$(printf '%s' "${uid}" | tr '-' '_')"
  for slice in /sys/fs/cgroup/kubepods.slice/kubepods-*.slice/kubepods-*-pod"${underscored}".slice; do
    [ -d "${slice}" ] || continue
    printf '  %s pod=%s\n' "${slice##*/}" "$(cat "${slice}/memory.swap.max")"
    for scope in "${slice}"/*.scope; do
      [ -d "${scope}" ] || continue
      printf '    container=%s\n' "$(cat "${scope}/memory.swap.max")"
    done
  done
done
SH
