#!/usr/bin/env bash
#
# Drive the queue experiment. Four modes, one owner: queue-lab.
#
# It needs no cluster for the first two, which is the point of the experiment's
# shape — the algorithm is decided in a simulator before anything is deployed.
#
#   scripts/queue-experiment.sh --simulate --mix 30s:1500,2m:900,5m:600 --rate 35
#   scripts/queue-experiment.sh --replay docs/session-lab-runs/<run>.db
#   scripts/queue-experiment.sh --live --queue=fifo
#   scripts/queue-experiment.sh --compare --queue=prio-aging --mix 30s:60,2m:30,5m:10 --rate 2
#
# --compare runs the control arm (session.Options.Queue left nil, so admission.Gate
# orders exactly as it always has) and then the named policy, with the same seed
# and therefore the same arrivals. Both arms run against namespace queue-lab and
# pool queue-lab-pool; nothing here touches sandbox-demo.
#
# --check-boundaries reports every path the branch has changed and fails when one
# is outside what this experiment owns. A change to research/, the queue package,
# cmd/queue-lab, config/queue-lab or this script is expected. A change to
# internal/runtime/admission, internal/runtime/slots, config/session-lab or the
# session-lab and persist drivers is a leak, and the point of this check is to
# catch it before it lands.
set -euo pipefail

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
app="${repo}/apps/session-lab"
out="${QUEUE_LAB_OUT:-${repo}/docs/queue-lab-runs}"
kubeconfig="${KUBECONFIG:-${HOME}/.kube/config}"
namespace="${QUEUE_LAB_NAMESPACE:-queue-lab}"
pool="${QUEUE_LAB_POOL:-queue-lab-pool}"

mode=""
mode_args=()

say() { printf '%s\n' "$*" >&2; }
warn() { printf 'WARNING: %s\n' "$*" >&2; }
die() { printf '%s\n' "$*" >&2; exit 1; }

usage() {
  awk 'NR > 2 && /^#/ { sub(/^# ?/, ""); print } NR > 2 && !/^#/ { exit }' "${BASH_SOURCE[0]}"
  exit "${1:-0}"
}

while (($# > 0)); do
  case "$1" in
    -h|--help) usage 0 ;;
    --simulate|--replay|--live|--compare|--check-boundaries)
      [[ -z "${mode}" ]] || die "one mode per run: ${mode} and $1"
      mode="${1#--}"
      shift
      continue
      ;;
    *) ;;
  esac
  mode_args+=("$1")
  shift
done

[[ -n "${mode}" ]] || { usage 2; }

run_lab() {
  ( cd "${app}" && go run ./cmd/queue-lab "$@" )
}

# --- boundary check --------------------------------------------------------------
# allowed: what the queue experiment may change.
allowed_prefixes=(
  "apps/session-lab/internal/queue/"
  "apps/session-lab/cmd/queue-lab/"
  "config/queue-lab/"
  "research/queue-scheduling/"
  "scripts/queue-experiment.sh"
  "queue_commit_message.sh"
)
# hook: files that may change only because the runner needs its one seam, and that
# are printed so a reviewer can see exactly what reached session-lab.
hook_paths=(
  "apps/session-lab/internal/runtime/session/queue.go"
  "apps/session-lab/internal/runtime/session/shared.go"
  "apps/session-lab/internal/runtime/session/run.go"
)
# defect: the one fix outside the experiment, and the reason it is here at all. Running
# the suite under -race to verify the queue work surfaced a concurrent map read and write
# on Runner.spools, which faults the whole process rather than merely racing. It is a
# crash-level defect in the code path this experiment drives, it is committed on its own
# as fix(session), and nothing else in these two files changed.
defect_paths=(
  "apps/session-lab/internal/runtime/session/pool.go"
  "apps/session-lab/internal/runtime/session/pool_test.go"
)
allowed_files=(
  "research/README.md"
)

check_boundaries() {
  local base="${1:-}"
  local changed path leaked=0 hooks=0 defects=0
  if [[ -z "${base}" ]]; then
    if git -C "${repo}" rev-parse --verify -q origin/main >/dev/null; then
      base="$(git -C "${repo}" merge-base HEAD origin/main)"
    else
      # No upstream to compare against: compare nothing rather than the whole
      # history, which would report every file the repository ever had.
      base="HEAD"
    fi
  fi
  say "boundary check against ${base} ($(git -C "${repo}" rev-parse --abbrev-ref HEAD))"
  changed="$(git -C "${repo}" diff --name-only "${base}"..HEAD)"
  if [[ -z "${changed}" ]]; then
    warn "no commits since ${base}; run this after the change is committed"
    return 0
  fi
  while IFS= read -r path; do
    [[ -n "${path}" ]] || continue
    local ok=0
    for prefix in "${allowed_prefixes[@]}"; do
      case "${path}" in "${prefix}"*) ok=1 ;; esac
    done
    for file in "${allowed_files[@]}"; do
      [[ "${path}" == "${file}" ]] && ok=1
    done
    for file in "${hook_paths[@]}"; do
      if [[ "${path}" == "${file}" ]]; then
        ok=1
        hooks=$((hooks + 1))
        say "  hook   ${path}"
      fi
    done
    for file in "${defect_paths[@]}"; do
      if [[ "${path}" == "${file}" ]]; then
        ok=1
        defects=$((defects + 1))
        say "  defect ${path}"
      fi
    done
    if ((ok == 0)); then
      printf '  LEAK   %s\n' "${path}" >&2
      leaked=$((leaked + 1))
    fi
  done <<<"${changed}"
  if ((leaked > 0)); then
    printf '\n%d path(s) outside the queue experiment. Move them, or extend the allow list in this\n' "${leaked}" >&2
    printf 'script and say why in research/queue-scheduling/02-boundaries-and-plan.md.\n' >&2
    return 1
  fi
  say "  ok: $(printf '%s\n' "${changed}" | wc -l) path(s), ${hooks} hook edit(s), ${defects} defect fix(es), nothing else"
}

# --- preflight (cluster modes) ---------------------------------------------------
preflight() {
  kubectl --kubeconfig "${kubeconfig}" get --raw=/readyz >/dev/null ||
    die "apiserver not ready; refusing to run"
  kubectl --kubeconfig "${kubeconfig}" get node --no-headers | grep -q ' Ready ' ||
    die "no Ready node; refusing to run"
  kubectl --kubeconfig "${kubeconfig}" get namespace "${namespace}" >/dev/null 2>&1 ||
    die "namespace ${namespace} does not exist; apply config/queue-lab first (helm template | kubectl apply -f -)"
  local replicas
  replicas="$(kubectl --kubeconfig "${kubeconfig}" -n "${namespace}" get sandboxwarmpool "${pool}" \
    -o jsonpath='{.spec.replicas}' 2>/dev/null || echo '?')"
  [[ "${replicas}" != "?" ]] || die "pool ${pool} not found in ${namespace}"
  say "control plane ok, node Ready, ${namespace}/${pool} replicas=${replicas}"
  ((replicas > 0)) || warn "pool ${pool} has 0 replicas: every session will pay a cold boot"
}

# cleanup_claims reports claims this experiment left behind. It never deletes on
# its own: a leaked claim is a finding, and the next run's preflight should see it.
cleanup_claims() {
  local left
  left="$(kubectl --kubeconfig "${kubeconfig}" -n "${namespace}" get sandboxclaims -o name 2>/dev/null |
    grep -c 'lab-' || true)"
  if [[ "${left}" != "0" ]]; then
    warn "${left} leaked queue-lab claim(s); delete with: kubectl -n ${namespace} delete sandboxclaims -l app.kubernetes.io/managed-by=session-lab"
    return 1
  fi
  say "no leaked claims in ${namespace}"
}

# The boundary check writes nothing, so it is the one mode that does not need an output
# directory and should not report one.
if [[ "${mode}" != "check-boundaries" ]]; then
  mkdir -p "${out}"
fi

case "${mode}" in
  simulate)
    say "queue-lab simulate: no cluster, no namespace, no claims"
    run_lab --simulate --queue all --out "${out}" "${mode_args[@]}"
    ;;
  replay)
    say "queue-lab replay: reads a recorded run, writes its own report beside it"
    run_lab --queue all --out "${out}" "${mode_args[@]}"
    ;;
  live)
    preflight
    say "queue-lab live: one arm, pool ${namespace}/${pool}"
    run_lab --live --out "${out}" --namespace "${namespace}" --pool "${pool}" \
      --kubeconfig "${kubeconfig}" "${mode_args[@]}"
    cleanup_claims
    ;;
  compare)
    preflight
    say "queue-lab compare: control arm first, then the named policy, same seed"
    run_lab --compare --out "${out}" --namespace "${namespace}" --pool "${pool}" \
      --kubeconfig "${kubeconfig}" "${mode_args[@]}"
    cleanup_claims
    ;;
  check-boundaries)
    if ((${#mode_args[@]} > 0)); then
      check_boundaries "${mode_args[0]}"
    else
      check_boundaries
    fi
    ;;
  *)
    die "unknown mode ${mode}"
    ;;
esac

[[ "${mode}" == "check-boundaries" ]] || say "output directory: ${out}"
