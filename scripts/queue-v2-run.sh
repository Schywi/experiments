#!/usr/bin/env bash
#
# Run the v2 arm of the capacity experiment and record what it did.
#
#   scripts/queue-v2-run.sh --tag=v2 --mix 30s:1500,2m:900,5m:600 --rate 35
#   scripts/queue-v2-run.sh --tag=v2 --sessions 300 --hold 30s --rate 4 --max-provisioning=4
#
# One arm, one scope: namespace queue-lab, pool queue-lab-pool, output in
# docs/queue-lab-runs. The session-lab pool and the persist runs are not touched.
#
# Before it starts it proves three things and prints them, because a run whose
# arm cannot be identified afterwards is not a measurement:
#
#   1. the autoscaler deployed in queue-lab is the tag you asked for, read back
#      from its own ConfigMap;
#   2. the pool starts where you think it starts (replicas, ready);
#   3. no claims are leaked from an earlier attempt.
#
# It ends by printing the three-way wait split (admission, boot permit, slot) and
# the redundant-boot count, which is what "my claim was worthless" looks like as a
# number.
#
# Kept in the repository on purpose: the run that produced a measurement has to be
# reproducible from the tree.
set -euo pipefail

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
app="${repo}/apps/session-lab"
out="${QUEUE_LAB_OUT:-${repo}/docs/queue-lab-runs}"
kubeconfig="${KUBECONFIG:-${HOME}/.kube/config}"
namespace="${QUEUE_LAB_NAMESPACE:-queue-lab}"
pool="${QUEUE_LAB_POOL:-queue-lab-pool}"
autoscaler_cm="${QUEUE_LAB_AUTOSCALER_CM:-queue-lab-autoscaler}"
demand_cm="${QUEUE_LAB_DEMAND_CM:-queue-lab-demand}"
tag=""
queue="prio-aging"
aging="30s"
admit="1"
max_provisioning="4"
classes="two"
class_normal="30s"
mix=""
sessions=""
hold="30s"
rate="2"
seed="1"
run_id=""
passthrough=()

say() { printf '%s\n' "$*" >&2; }
warn() { printf 'WARNING: %s\n' "$*" >&2; }
die() { printf '%s\n' "$*" >&2; exit 1; }

usage() {
  awk 'NR > 2 && /^#/ { sub(/^# ?/, ""); print } NR > 2 && !/^#/ { exit }' "${BASH_SOURCE[0]}"
  exit "${1:-0}"
}

dry_run=0
while (($# > 0)); do
  case "$1" in
    -h|--help) usage 0 ;;
    --dry-run) dry_run=1; shift ;;
    --tag=*) tag="${1#*=}"; shift ;;
    --tag) tag="${2:?--tag needs a value}"; shift 2 ;;
    --queue=*) queue="${1#*=}"; shift ;;
    --queue) queue="${2:?--queue needs a value}"; shift 2 ;;
    --aging=*) aging="${1#*=}"; shift ;;
    --aging) aging="${2:?--aging needs a value}"; shift 2 ;;
    --admit=*) admit="${1#*=}"; shift ;;
    --admit) admit="${2:?--admit needs a value}"; shift 2 ;;
    --max-provisioning=*) max_provisioning="${1#*=}"; shift ;;
    --max-provisioning) max_provisioning="${2:?--max-provisioning needs a value}"; shift 2 ;;
    --classes=*) classes="${1#*=}"; shift ;;
    --classes) classes="${2:?--classes needs a value}"; shift 2 ;;
    --class-normal=*) class_normal="${1#*=}"; shift ;;
    --class-normal) class_normal="${2:?--class-normal needs a value}"; shift 2 ;;
    --mix=*) mix="${1#*=}"; shift ;;
    --mix) mix="${2:?--mix needs a value}"; shift 2 ;;
    --sessions=*) sessions="${1#*=}"; shift ;;
    --sessions) sessions="${2:?--sessions needs a value}"; shift 2 ;;
    --hold=*) hold="${1#*=}"; shift ;;
    --hold) hold="${2:?--hold needs a value}"; shift 2 ;;
    --rate=*) rate="${1#*=}"; shift ;;
    --rate) rate="${2:?--rate needs a value}"; shift 2 ;;
    --seed=*) seed="${1#*=}"; shift ;;
    --seed) seed="${2:?--seed needs a value}"; shift 2 ;;
    --run-id=*) run_id="${1#*=}"; shift ;;
    --run-id) run_id="${2:?--run-id needs a value}"; shift 2 ;;
    --) shift; passthrough+=("$@"); break ;;
    --*) passthrough+=("$1"); shift ;;
    *) passthrough+=("$1"); shift ;;
  esac
done

[[ -n "${tag}" ]] || die "--tag is required: it is the arm, and a run without one is not a measurement"

k() { kubectl --kubeconfig "${kubeconfig}" "$@"; }

# --- preflight --------------------------------------------------------------------
say "==> preflight"
k get --raw=/readyz >/dev/null || die "apiserver not ready; refusing to run"
k get node --no-headers | grep -q ' Ready ' || die "no Ready node; refusing to run"
k get namespace "${namespace}" >/dev/null 2>&1 ||
  die "namespace ${namespace} does not exist; apply config/queue-lab first"

replicas="$(k -n "${namespace}" get sandboxwarmpool "${pool}" -o jsonpath='{.spec.replicas}' 2>/dev/null || echo '')"
ready="$(k -n "${namespace}" get sandboxwarmpool "${pool}" -o jsonpath='{.status.readyReplicas}' 2>/dev/null || echo '')"
[[ -n "${replicas}" ]] || die "pool ${pool} not found in ${namespace}"
say "    pool ${namespace}/${pool}: replicas=${replicas} ready=${ready:-0}"

# The arm has to be readable back, or the run cannot be attributed afterwards.
deployed_tag="$(k -n "${namespace}" get configmap "${autoscaler_cm}" -o jsonpath='{.data.settings\.json}' 2>/dev/null |
  sed -n 's/.*"image_tag": *"\([^"]*\)".*/\1/p' || true)"
if [[ -z "${deployed_tag}" ]]; then
  warn "no image_tag published in configmap ${autoscaler_cm}: the autoscaler may be v1, which does not publish one"
  warn "  asking for ${tag}; continuing, but this run cannot prove which arm it ran"
elif [[ "${deployed_tag}" != "${tag}" ]]; then
  die "deployed autoscaler is ${deployed_tag}, you asked for ${tag}; apply the chart with that tag first"
else
  say "    autoscaler arm: ${deployed_tag} (read back from ${autoscaler_cm})"
fi

leaked="$(k -n "${namespace}" get sandboxclaims -o name 2>/dev/null | grep -c 'lab-' || true)"
if [[ "${leaked}" != "0" ]]; then
  die "${leaked} claim(s) already in ${namespace}; clean them before a measured run:
       kubectl -n ${namespace} delete sandboxclaims -l app.kubernetes.io/managed-by=session-lab"
fi
say "    no leaked claims"

mkdir -p "${out}"

# --- the run ----------------------------------------------------------------------
workload=(--rate "${rate}")
if [[ -n "${mix}" ]]; then
  workload+=(--mix "${mix}")
elif [[ -n "${sessions}" ]]; then
  workload+=(--sessions "${sessions}" --hold "${hold}")
else
  die "give --mix or --sessions to define the workload"
fi

args=(
  --live
  --queue="${queue}" --aging="${aging}" --classes="${classes}" --class-normal="${class_normal}"
  --admit="${admit}" --max-provisioning="${max_provisioning}"
  --publish-demand="${namespace}/${demand_cm}"
  --seed="${seed}" --out "${out}"
  --namespace "${namespace}" --pool "${pool}" --kubeconfig "${kubeconfig}"
)
[[ -n "${run_id}" ]] && args+=(--run-id "${run_id}")
if ((${#passthrough[@]} > 0)); then
  for arg in "${passthrough[@]}"; do
    [[ -n "${arg}" ]] && args+=("${arg}")
  done
fi

if ((dry_run == 1)); then
  printf 'would run:'
  printf ' %q' go run ./cmd/queue-lab "${args[@]}" "${workload[@]}"
  printf '\n'
  exit 0
fi

say "==> queue-lab ${args[*]} ${workload[*]}"
( cd "${app}" && go run ./cmd/queue-lab "${args[@]}" "${workload[@]}" )

# --- what the arm did -------------------------------------------------------------
say
say "==> arm record"
k -n "${namespace}" get configmap "${autoscaler_cm}" -o jsonpath='{.data.settings\.json}' 2>/dev/null |
  sed -n 's/^/    /p' | head -40

say
say "==> queue depth last published"
k -n "${namespace}" get configmap "${demand_cm}" -o jsonpath='{.data.demand\.json}' 2>/dev/null |
  sed -n 's/^/    /p' | head -20

left="$(k -n "${namespace}" get sandboxclaims -o name 2>/dev/null | grep -c 'lab-' || true)"
if [[ "${left}" != "0" ]]; then
  warn "${left} leaked claim(s) after the run:
       kubectl -n ${namespace} delete sandboxclaims -l app.kubernetes.io/managed-by=session-lab"
  exit 3
fi
say "    no leaked claims after the run"
say "    output: ${out}"
