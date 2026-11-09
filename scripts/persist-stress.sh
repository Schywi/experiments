#!/usr/bin/env bash
#
# One persistence stress experiment against MinIO.
#
# Two arms of the same workload — control (nothing persists) and persistence
# (a fraction persists, with seeded state, under a byte budget) — and then the
# store itself is asked how many objects it holds for each run. The script
# refuses to call a run good when the store disagrees with the writer's counter.
#
#   scripts/persist-stress.sh
#   scripts/persist-stress.sh --sessions 600 --rate 6 --hold 40s --persist-rate 0.5
#   scripts/persist-stress.sh --seed-bytes 262144 --persist-budget 67108864
#   scripts/persist-stress.sh --no-control
#
# Every knob is also an env var, so a long run can be shaped without editing:
#   SESSIONS RATE HOLD PERSIST_RATE RESTORE_RATE PERSIST_BUDGET
#   SEED_BYTES SEED_COOKIES CONTEXTS_PER_BROWSER
#   PERSIST_OUT PERSIST_POOL PERSIST_NAMESPACE PERSIST_TARGET_URL PERSIST_CONSOLE
#
# It never deletes a blob except through the budget's own oldest-first eviction,
# and it never touches a claim it did not create.
set -euo pipefail

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
app="${repo}/apps/session-lab"
out="${PERSIST_OUT:-${repo}/docs/session-lab-runs}"
kubeconfig="${KUBECONFIG:-${HOME}/.kube/config}"
namespace="${PERSIST_NAMESPACE:-sandbox-demo}"
pool="${PERSIST_POOL:-browser-pool}"
target="${PERSIST_TARGET_URL:-https://example.com/}"
console="${PERSIST_CONSOLE:-http://192.168.0.252}"

sessions="${SESSIONS:-300}"
rate="${RATE:-3}"
hold="${HOLD:-30s}"
persist_rate="${PERSIST_RATE:-0.2}"
restore_rate="${RESTORE_RATE:-0.3}"
budget="${PERSIST_BUDGET:-33554432}"
seed_bytes="${SEED_BYTES:-65536}"
seed_cookies="${SEED_COOKIES:-2}"
contexts="${CONTEXTS_PER_BROWSER:-8}"
control=1

while [[ $# -gt 0 ]]; do
  case "$1" in
    --sessions) sessions="$2"; shift 2 ;;
    --rate) rate="$2"; shift 2 ;;
    --hold) hold="$2"; shift 2 ;;
    --persist-rate) persist_rate="$2"; shift 2 ;;
    --restore-rate) restore_rate="$2"; shift 2 ;;
    --persist-budget) budget="$2"; shift 2 ;;
    --seed-bytes) seed_bytes="$2"; shift 2 ;;
    --seed-cookies) seed_cookies="$2"; shift 2 ;;
    --contexts-per-browser) contexts="$2"; shift 2 ;;
    --target-url) target="$2"; shift 2 ;;
    --pool) pool="$2"; shift 2 ;;
    --no-control) control=0; shift ;;
    -h|--help) sed -n '2,22p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown flag: $1" >&2; exit 2 ;;
  esac
done

say()  { printf '%s stress: %s\n' "$(date -u +%H:%M:%S)" "$*"; }
fail() { printf '%s stress: ERROR %s\n' "$(date -u +%H:%M:%S)" "$*" >&2; exit 1; }

mkdir -p "${out}"
log_file="${out}/persist-stress-$(date -u +%Y%m%dT%H%M%SZ).log"
exec > >(tee -a "${log_file}") 2>&1
say "log file: ${log_file}"

# A run that begins during an outage records a control-plane failure as if it
# were a storage result.
echo "==> control plane"
kubectl get --raw=/readyz >/dev/null || fail "apiserver not ready"
kubectl get node --no-headers | grep -q ' Ready ' || fail "no Ready node"
say "control plane ok, pool ${namespace}/${pool}"

echo "==> build"
( cd "${app}" && go build -o "${out}/persist-lab" ./cmd/persist-lab )

# field <file> <key> reads one number out of a run's JSON artefact, 0 if absent.
field() {
  python3 - "$1" "$2" <<'PY'
import json, os, sys
path, key = sys.argv[1], sys.argv[2]
if not os.path.exists(path):
    print(0); raise SystemExit
with open(path) as handle:
    print(json.load(handle).get(key, 0))
PY
}

# objects_on_store <run-id> counts what MinIO itself holds. MinIO keeps one
# directory per object with an xl.meta inside, so this is the store's view rather
# than the writer's counter.
objects_on_store() {
  local run_id=$1 count
  count="$(kubectl -n "${namespace}" exec deploy/minio -- \
    sh -c "find /data/session-lab/runs/${run_id} -name xl.meta 2>/dev/null | wc -l" 2>/dev/null | tr -d '[:space:]')"
  printf '%s' "${count:-0}"
}

# run_arm <label> <persist-rate> <restore-rate> <budget>; the run id is left in
# ${out}/.last-run-id because a command substitution would swallow the log.
run_arm() {
  local label=$1 pr=$2 rr=$3 bud=$4 run_id
  run_id="$(date -u +%Y%m%dT%H%M%SZ)-${label}"
  printf '%s' "${run_id}" > "${out}/.last-run-id"
  say "arm ${label}: ${sessions} sessions at ${rate}/s, hold ${hold}, persist ${pr}, restore ${rr}, budget ${bud}, seed ${seed_bytes}B + ${seed_cookies} cookies"
  "${out}/persist-lab" \
      --sessions "${sessions}" --rate "${rate}" --hold "${hold}" \
      --contexts-per-browser "${contexts}" \
      --persist-rate "${pr}" --persist-budget "${bud}" --restore-rate "${rr}" \
      --seed-bytes "${seed_bytes}" --seed-cookies "${seed_cookies}" \
      --target-url "${target}" \
      --namespace "${namespace}" --pool "${pool}" \
      --kubeconfig "${kubeconfig}" --out "${out}" --run-id "${run_id}"
}

mismatch=0

arm_report() {
  local label=$1 run_id=$2
  local blobs ingest evicted refused ok failed stored
  blobs="$(field "${out}/${run_id}-persist.json" blobs)"
  ingest="$(field "${out}/${run_id}-persist.json" ingest_bytes)"
  evicted="$(field "${out}/${run_id}-persist.json" evicted_bytes)"
  refused="$(field "${out}/${run_id}-persist.json" refused)"
  ok="$(field "${out}/${run_id}-restore.json" ok)"
  failed="$(field "${out}/${run_id}-restore.json" failed)"
  stored="$(objects_on_store "${run_id}")"
  printf 'arm=%-8s blobs=%-6s ingest=%-10s evicted=%-9s refused=%-4s restore_ok=%-5s restore_failed=%-4s objects_on_store=%-5s run=%s\n' \
    "${label}" "${blobs}" "${ingest}" "${evicted}" "${refused}" "${ok}" "${failed}" "${stored}" "${run_id}"
  if [[ "${blobs}" != "${stored}" ]]; then
    say "MISMATCH ${label}: writer counted ${blobs} blobs, MinIO holds ${stored}"
    mismatch=1
  fi
}

control_id=""
persist_id=""

if (( control )); then
  echo "==> arm: control (nothing persists)"
  run_arm control 0 0 0
  control_id="$(cat "${out}/.last-run-id")"
  arm_report control "${control_id}"
fi

echo "==> arm: persistence"
run_arm persist "${persist_rate}" "${restore_rate}" "${budget}"
persist_id="$(cat "${out}/.last-run-id")"
arm_report persist "${persist_id}"

echo "==> store"
if command -v curl >/dev/null 2>&1; then
  say "bucket state: $(curl -s --max-time 10 "${console}/api/v1/buckets" | head -c 400)"
fi

echo "==> cleanup"
left="$(kubectl -n "${namespace}" get sandboxclaims -o name 2>/dev/null | grep -c 'lab-' || true)"
kubectl -n "${namespace}" delete sandboxclaims \
  -l app.kubernetes.io/managed-by=session-lab --wait=false 2>/dev/null || true
say "leftover claims deleted: ${left}"
rm -f "${out}/.last-run-id"

echo
if (( mismatch )); then
  fail "a blob count disagrees with the store; reports under ${out}"
fi
say "both arms agree with the store"
say "control=${control_id:-none} persist=${persist_id}"
