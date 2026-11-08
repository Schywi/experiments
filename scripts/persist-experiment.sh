#!/usr/bin/env bash
#
# Run one persistence experiment and print what it did.
#
# This is a separate driver from scripts/session-lab-experiment.sh, which keeps
# the behaviour it was designed for. Both call the same session.Run; this one
# adds the capture arm and reports the bytes it wrote.
#
# Twenty percent of sessions persist, blobs under the run's own directory:
#   scripts/persist-experiment.sh --sessions 50 --rate 1 --hold 20s --persist-rate 0.2
#
# The same workload with no persistence, which is the control arm:
#   scripts/persist-experiment.sh --sessions 50 --rate 1 --hold 20s --persist-rate 0
#
# A budget that forces evictions partway through:
#   scripts/persist-experiment.sh --sessions 200 --rate 2 --hold 20s \
#       --persist-rate 0.5 --persist-budget 1048576
#
# Blobs go to a local directory under --out by default. To send them to the
# MinIO this chart deploys instead, set the endpoint and the credentials read
# from the Secret:
#   PERSIST_S3_ENDPOINT=192.168.0.150:30900 \
#   PERSIST_S3_ACCESS_KEY=... PERSIST_S3_SECRET_KEY=... \
#       scripts/persist-experiment.sh --sessions 50 --rate 1 --hold 20s \
#           --persist-rate 0.2 --restore-rate 0.2
#
# --restore-rate boots that fraction of the stored blobs on a fresh claim after
# the run, which is the "a session starts from a stored blob" claim.
#
# Refuses to start if the control plane is down, because a run that begins during
# an outage records a control-plane failure as if it were a storage result.
set -euo pipefail

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
app="${repo}/apps/session-lab"
out="${PERSIST_OUT:-${repo}/docs/session-lab-runs}"
kubeconfig="${KUBECONFIG:-${HOME}/.kube/config}"
namespace="${PERSIST_NAMESPACE:-sandbox-demo}"
pool="${PERSIST_POOL:-browser-pool}"

usage() {
  sed -n '2,25p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
  exit "${1:-0}"
}

main() {
  [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]] && usage 0
  [[ $# -ge 1 ]] || usage 2

  say()  { printf '%s persist: %s\n' "$(date -u +%H:%M:%S)" "$*"; }
  fail() { printf '%s persist: ERROR %s\n' "$(date -u +%H:%M:%S)" "$*" >&2; }

  mkdir -p "${out}"
  log_file="${out}/persist-script-$(date -u +%Y%m%dT%H%M%SZ).log"
  exec > >(tee -a "${log_file}") 2>&1
  say "log file: ${log_file}"
  say "args: $*"

  echo "==> control plane"
  kubectl get --raw=/readyz >/dev/null || { fail "apiserver not ready; refusing to run"; exit 1; }
  kubectl get node --no-headers | grep -q ' Ready ' || { fail "no Ready node; refusing to run"; exit 1; }
  say "control plane ok"

  echo "==> build"
  ( cd "${app}" && go build -o "${out}/persist-lab" ./cmd/persist-lab )

  # The blob directory is per run, so its size answers "how much did this run
  # write" without opening the report.
  persist_dir="${out}/persist-blobs"

  # Where the blobs go: MinIO when PERSIST_S3_ENDPOINT is set, the local
  # directory otherwise. The directory arm is the control the MinIO arm is
  # compared against.
  store_args=()
  if [[ -n "${PERSIST_S3_ENDPOINT:-}" ]]; then
    store_args+=(--persist-s3-endpoint "${PERSIST_S3_ENDPOINT}")
    store_args+=(--persist-s3-bucket "${PERSIST_S3_BUCKET:-session-lab}")
    # Explicit ifs, not `[[ ]] && append`: a false test is the last command of
    # the line and would abort the script under set -e.
    if [[ -n "${PERSIST_S3_ACCESS_KEY:-}" ]]; then
      store_args+=(--persist-s3-access-key "${PERSIST_S3_ACCESS_KEY}")
    fi
    if [[ -n "${PERSIST_S3_SECRET_KEY:-}" ]]; then
      store_args+=(--persist-s3-secret-key "${PERSIST_S3_SECRET_KEY}")
    fi
    say "blobs: s3://${PERSIST_S3_BUCKET:-session-lab} at ${PERSIST_S3_ENDPOINT}"
  else
    store_args+=(--persist-dir "${persist_dir}")
    say "blobs: ${persist_dir}"
  fi

  echo "==> run"
  say "command: persist-lab $*"
  "${out}/persist-lab" "$@" \
      --namespace "${namespace}" --pool "${pool}" \
      --kubeconfig "${kubeconfig}" --out "${out}" \
      "${store_args[@]}"

  echo "==> artefacts"
  newest_report=$(ls -t "${out}"/*.json 2>/dev/null | head -1 || true)
  say "report:  ${newest_report}"
  newest_stats=$(ls -t "${out}"/*-persist.json 2>/dev/null | head -1 || true)
  say "storage: ${newest_stats}"
  if [[ -f "${newest_stats}" ]]; then
    cat "${newest_stats}"
  fi
  newest_restore=$(ls -t "${out}"/*-restore.json 2>/dev/null | head -1 || true)
  if [[ -n "${newest_restore}" ]]; then
    say "restore: ${newest_restore}"
    cat "${newest_restore}"
  fi
  if [[ -z "${PERSIST_S3_ENDPOINT:-}" ]]; then
    say "blob directory: $(du -sh "${persist_dir}" 2>/dev/null | cut -f1) in ${persist_dir}"
    say "blob count: $(find "${persist_dir}" -name '*.json.gz' 2>/dev/null | wc -l)"
  fi

  echo "==> cleanup"
  left=$(kubectl -n "${namespace}" get sandboxclaims -o name 2>/dev/null | grep -c 'lab-' || true)
  kubectl -n "${namespace}" delete sandboxclaims \
    -l app.kubernetes.io/managed-by=session-lab --wait=false 2>/dev/null || true
  if [[ "${left}" != "0" ]]; then
    say "deleted ${left} leftover claims"
  else
    say "no leftover claims"
  fi
}

main "$@"
