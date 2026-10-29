#!/usr/bin/env bash
# Run one arm end to end, and only one.
#
#   bash run_arm.sh smolvlm2
#   bash run_arm.sh moondream
#   bash run_arm.sh qwen3vl
#
# What it does, in order, with a guard between each step:
#
#   1. refuse to start if any other arm's Pod exists in vlm-exp
#   2. scale this arm to 1 and wait for it to be Ready
#   3. run the runner Job against http://vlm-<arm>:8000
#   4. write the Job's log to results/<arm>.log
#   5. scale the arm to 0 and wait for its Pod to be gone
#
# Step 5 runs on the way out even if step 3 or 4 fails, so a failed arm does not
# keep holding the render device.
set -euo pipefail

arm="${1:?usage: run_arm.sh <smolvlm2|moondream|qwen3vl>}"
case "${arm}" in
  smolvlm2|moondream|qwen3vl) ;;
  *) echo "unknown arm: ${arm}" >&2; exit 2 ;;
esac

here="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
cd -- "${here}"
ns=vlm-exp
job="e5-run-${arm}"
log="results/${arm}.log"

# 1. the sequential rule, checked rather than assumed.
#    Only Running/Pending pods count: a finished runner Job's pod keeps the arm
#    label after it completes, and that must not block the next arm.
others="$(kubectl -n "${ns}" get pods -l 'app.kubernetes.io/part-of=vlm-lab,arm' \
  -o jsonpath='{range .items[?(@.status.phase=="Running")]}{.metadata.labels.arm}{"\n"}{end}{range .items[?(@.status.phase=="Pending")]}{.metadata.labels.arm}{"\n"}{end}' \
  | grep -v "^${arm}$" | grep -v '^$' || true)"
if [[ -n "${others}" ]]; then
  echo "refusing to run ${arm}: another arm is up (${others//$'\n'/ })" >&2
  exit 1
fi

cleanup() {
  echo "==> scaling ${arm} to 0"
  kubectl -n "${ns}" scale "deploy/vlm-${arm}" --replicas=0 >/dev/null
  kubectl -n "${ns}" wait --for=delete pod -l "arm=${arm}" --timeout=180s >/dev/null 2>&1 || true
  kubectl -n "${ns}" get pods --no-headers || true
}
trap cleanup EXIT

# The three PNGs go onto the `vlm-frames` claim once and are then mounted
# read-only by every arm, so all three arms and every rerun see the same bytes.
# A short-lived busybox pod is used because `kubectl cp` needs a container with
# tar in it; the quota wants resource requests on that container, hence the
# explicit overrides rather than a bare `kubectl run`.
ensure_frames() {
  local loader=e5-frames-loader
  if kubectl -n "${ns}" get pvc vlm-frames >/dev/null 2>&1 &&
     kubectl -n "${ns}" get pod "${loader}" >/dev/null 2>&1 &&
     kubectl -n "${ns}" exec "${loader}" -- test -f /frames/console-home.png >/dev/null 2>&1; then
    echo "==> frames already on the vlm-frames claim"
    kubectl -n "${ns}" delete pod "${loader}" --wait=true >/dev/null 2>&1 || true
    return 0
  fi
  echo "==> copying results/frames/*.png onto the vlm-frames claim"
  kubectl -n "${ns}" delete pod "${loader}" --ignore-not-found >/dev/null 2>&1 || true
  kubectl -n "${ns}" run "${loader}" --image=busybox:1.36 --restart=Never \
    --overrides='{"spec":{"containers":[{"name":"loader","image":"busybox:1.36","command":["sleep","600"],
      "resources":{"requests":{"cpu":"50m","memory":"64Mi"},"limits":{"cpu":"200m","memory":"128Mi"}},
      "volumeMounts":[{"name":"frames","mountPath":"/frames"}]}],
      "volumes":[{"name":"frames","persistentVolumeClaim":{"claimName":"vlm-frames"}}]}}' >/dev/null
  kubectl -n "${ns}" wait --for=condition=Ready "pod/${loader}" --timeout=180s >/dev/null
  kubectl -n "${ns}" cp results/frames/. "${loader}":/frames/
  kubectl -n "${ns}" exec "${loader}" -- ls -l /frames
  kubectl -n "${ns}" delete pod "${loader}" --wait=true >/dev/null
}

# fixtures: runner.py + the task/label files, and the PNG bytes every arm sees
kubectl -n "${ns}" create configmap e5-runner \
  --from-file=runner.py=runner.py \
  --from-file=tasks.json=fixtures/tasks.json \
  --from-file=labels.json=fixtures/labels.json \
  --dry-run=client -o yaml | kubectl apply -f - >/dev/null
ensure_frames

# 2. the arm
echo "==> scaling ${arm} to 1"
kubectl -n "${ns}" scale "deploy/vlm-${arm}" --replicas=1 >/dev/null
kubectl -n "${ns}" rollout status "deploy/vlm-${arm}" --timeout=600s

# 3. the runner, one Job with this arm's name and url
echo "==> running ${job}"
kubectl -n "${ns}" delete job "${job}" --ignore-not-found >/dev/null
sed -e "s|e5-run-smolvlm2|${job}|g" \
    -e "s|value: smolvlm2|value: ${arm}|g" \
    -e "s|http://vlm-smolvlm2:8000|http://vlm-${arm}:8000|g" \
    manifests/runner-job.yaml | kubectl -n "${ns}" create -f - >/dev/null

set +e
kubectl -n "${ns}" wait --for=condition=complete "job/${job}" --timeout=3600s
status=$?
set -e

# 4. the log is the result
mkdir -p results
kubectl -n "${ns}" logs "job/${job}" >"${log}" 2>&1 || true
echo "==> wrote ${log} ($(wc -l <"${log}") lines)"
grep -c '^ROW ' "${log}" || true
grep -o '"ok": [0-9]*' "${log}" | tail -1 || true

exit "${status}"
