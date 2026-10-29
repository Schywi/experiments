#!/usr/bin/env bash
#
# Run one session-lab experiment and print what it did.
#
# Ten-minute window, mixed lengths (the acceptance experiment):
#   scripts/session-lab-experiment.sh --duration 600s --rate 0.1 \
#       --mix 30s:40,2m:15,5m:5 --policy reuse
#
# Ten-minute window, one length:
#   scripts/session-lab-experiment.sh --duration 600s --rate 0.1 --hold 30s
#
# Fixed session count, mixed lengths:
#   scripts/session-lab-experiment.sh --sessions 20 --mix 30s:10,2m:10 --rate 0.3
#
# Destroy/rebuild instead of reuse/reset:
#   scripts/session-lab-experiment.sh --duration 600s --rate 0.1 --hold 60s --policy destroy
#
# Refuses to start if the control plane is unhealthy, because a run that begins
# during an outage records a control-plane failure as if it were a capacity
# result. Prints the run's report path and then checks for leaked claims.
set -euo pipefail

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
app="${repo}/apps/session-lab"
out="${SESSION_LAB_OUT:-${repo}/docs/session-lab-runs}"
kubeconfig="${KUBECONFIG:-${HOME}/.kube/config}"
namespace="${SESSION_LAB_NAMESPACE:-sandbox-demo}"
pool="${SESSION_LAB_POOL:-browser-pool}"

usage() {
  sed -n '2,20p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
  exit "${1:-0}"
}

# Check the arguments here, so a bad combination fails in a second instead of
# after a build and a cluster preflight.
validate_args() {
  local has_duration=0 has_rate=0 has_mix=0 has_hold=0 has_sessions=0 arg
  for arg in "$@"; do
    case "${arg}" in
      --duration|--duration=*) has_duration=1 ;;
      --rate|--rate=*) has_rate=1 ;;
      --mix|--mix=*) has_mix=1 ;;
      --hold|--hold=*) has_hold=1 ;;
      --sessions|--sessions=*) has_sessions=1 ;;
    esac
  done
  if (( has_duration )); then
    (( has_rate )) || { echo "--duration needs --rate (sessions = rate x duration)" >&2; usage 2; }
    (( has_mix || has_hold )) || { echo "--duration needs --mix or --hold to set the session length" >&2; usage 2; }
  else
    (( has_sessions || has_mix )) || { echo "give --duration, or --sessions/--mix" >&2; usage 2; }
  fi
}

[[ "${1:-}" == "-h" || "${1:-}" == "--help" ]] && usage 0
[[ $# -ge 1 ]] || usage 2
validate_args "$@"

echo "==> control plane"
kubectl get --raw=/readyz >/dev/null || { echo "apiserver not ready; refusing to run" >&2; exit 1; }
kubectl get node --no-headers | grep -q ' Ready ' || { echo "no Ready node; refusing to run" >&2; exit 1; }
echo "    apiserver ok, node Ready, pool replicas=$(kubectl -n "${namespace}" get sandboxwarmpool "${pool}" -o jsonpath='{.spec.replicas}' 2>/dev/null || echo '?')"

echo "==> build"
( cd "${app}" && go build -o "${out}/session-lab" ./cmd/session-lab )
mkdir -p "${out}"

echo "==> run"
set -x
"${out}/session-lab" "$@" --namespace "${namespace}" --pool "${pool}" \
    --kubeconfig "${kubeconfig}" --out "${out}"
set +x

echo "==> afterwards"
left=$(kubectl -n "${namespace}" get sandboxclaims -o name 2>/dev/null | grep -c 'lab-' || true)
echo "    leaked lab- claims: ${left}"
echo "    pool replicas now: $(kubectl -n "${namespace}" get sandboxwarmpool "${pool}" -o jsonpath='{.spec.replicas}')"
echo "    paused annotation: \"$(kubectl -n "${namespace}" get sandboxwarmpool "${pool}" -o jsonpath='{.metadata.annotations.session-lab\.io/autoscaler}')\""
echo "    newest report: $(ls -t "${out}"/*.json | head -1)"
[[ "${left}" == "0" ]] || { echo "    WARNING: leaked claims; delete with: kubectl -n ${namespace} delete sandboxclaims -l app.kubernetes.io/managed-by=session-lab" >&2; exit 3; }
