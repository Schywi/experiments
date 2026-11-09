#!/usr/bin/env bash
#
# P1: sweep the pool's provisioning bound and measure boot throughput.
#
#   scripts/queue-boot-sweep.sh --tag persist-probe --ks 1,2,4,8
#
# The experiment varies exactly one thing: slots.Options.MaxProvisioning, passed as
# --max-provisioning. Everything that could move the answer is pinned:
#
#   - the warm pool's replicas come from the chart and nothing scales them
#     (autoscaler.enabled=false), so capacity is the image under test, not a controller;
#   - --boot-concurrency=-1, because 0 means "derive it from a past measured peak" in
#     preflight, and a stale peak would silently re-serialise the boot permit so every
#     arm measured k=1 while claiming otherwise;
#   - the same workload, seed and starting replica count for every arm.
#
# k is measured, not asserted: after each arm the run's own rows go through
# `queue-lab --boot-report`, which groups boots by how many were in flight at the
# same time according to their created/ready timestamps. If the pool serialises, the
# report says k=1 whatever the flag was.
set -euo pipefail

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${repo}"

tag=""
ks="1,2,4"
mix="10s:120,30s:60"
rate="4"
class_normal="10s"
out="${QUEUE_LAB_OUT:-${repo}/docs/queue-lab-runs}/boot"
apply=0

say() { printf '%s\n' "$*" >&2; }
die() { printf '%s\n' "$*" >&2; exit 1; }

while (($# > 0)); do
  case "$1" in
    -h|--help) awk 'NR > 2 && /^#/ { sub(/^# ?/, ""); print } NR > 2 && !/^#/ { exit }' "${BASH_SOURCE[0]}"; exit 0 ;;
    --tag) tag="${2:?--tag needs a value}"; shift 2 ;;
    --ks) ks="${2:?--ks needs a value}"; shift 2 ;;
    --mix) mix="${2:?--mix needs a value}"; shift 2 ;;
    --rate) rate="${2:?--rate needs a value}"; shift 2 ;;
    --class-normal) class_normal="${2:?--class-normal needs a value}"; shift 2 ;;
    --apply) apply=1; shift ;;
    *) die "unknown argument $1 (see --help)" ;;
  esac
done

[[ -n "${tag}" ]] || die "--tag is required; it names the arm and every run records it"

# --- the pool is pinned, the autoscaler is off --------------------------------------
say "==> pinning the pool: autoscaler.enabled=false, replicas from the chart"
if ((apply == 1)); then
  helm template queue-lab config/queue-lab \
    --set autoscaler.enabled=false \
    --set "autoscaler.image.tag=${tag}" | kubectl apply -f -
else
  say "    (dry) helm template queue-lab config/queue-lab --set autoscaler.enabled=false | kubectl apply -f -"
fi

replicas="$(kubectl -n queue-lab get sandboxwarmpool queue-lab-pool -o jsonpath='{.spec.replicas}' 2>/dev/null || echo '?')"
say "    queue-lab-pool replicas=${replicas} (fixed for every arm)"

# --- one arm per k ------------------------------------------------------------------
mkdir -p "${out}"
IFS=',' read -r -a schools <<<"${ks}"
for k in "${schools[@]}"; do
  run_id="boot-k${k}-$(date -u +%Y%m%dT%H%M%SZ)"
  say
  say "==> arm k=${k}: run ${run_id}"
  QUEUE_LAB_OUT="${out}" bash "${repo}/scripts/queue-v2-run.sh" \
    "--tag=${tag}" \
    "--run-id=${run_id}" \
    "--max-provisioning=${k}" \
    "--classes=two" "--class-normal=${class_normal}" \
    "--mix=${mix}" "--rate=${rate}" \
    -- --boot-concurrency=-1

  db="${out}/${run_id}.db"
  if [[ -f "${db}" ]]; then
    say "==> measured boot report for k=${k}"
    ( cd apps/session-lab && go run ./cmd/queue-lab --boot-report "${db}" --out "${out}" --run-id "${run_id}" )
  else
    say "    no run database at ${db}; the run did not write one"
  fi
done

say
say "read the k column of each report: that is the concurrency the pool achieved,"
say "not the one it was asked for. If every arm reports k=1, the pool serialises and"
say "the provisioning bound is not the lever."
