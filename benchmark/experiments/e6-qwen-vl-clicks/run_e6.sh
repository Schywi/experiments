#!/usr/bin/env bash
# E6 orchestration: fixtures onto a claim, one model resident, one controller Job.
#
#   bash run_e6.sh --dry-run                       # all sets, no click dispatched
#   bash run_e6.sh                                 # all sets, vision-only, real clicks
#   bash run_e6.sh --arm dom-unique --sets baseline
#   bash run_e6.sh --calibrate                     # refit the coordinate transform only
#
# Every stage prints what it did and what the cluster answered. The Job's log is
# streamed, so a run of any length is observable in the terminal that started it.
#
# The model is scaled to 1 for the run and back to 0 on the way out, including when
# the run fails: the node has one render device and leaving it held would block the
# next attempt. The guard in step 1 refuses to start while any other model Pod in
# this namespace is Running or Pending.
set -euo pipefail

here="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
cd -- "${here}"

ns=vlm-e6
model_deploy=qwen3vl
sets="baseline,discriminate,negative,determinism"
arm="vision-only"
# The page every task in the run navigates to before it asks. The console grid
# unless a set is defined against the served fixture page.
page_url="http://192.168.0.250:8080/"
dry_run=0
calibrate=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run) dry_run=1 ;;
    --calibrate) calibrate=1 ;;
    --sets) shift; sets="$1" ;;
    --arm) shift; arm="$1" ;;
    --page-url) shift; page_url="$1" ;;
    -h|--help) sed -n '2,14p' "$0"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
  shift
done

step() { printf '\n== %s\n' "$*"; }

# --------------------------------------------------------------------------- #
# 1. one model at a time, checked rather than assumed
# --------------------------------------------------------------------------- #
step "checking no other model arm is up"
live="$(kubectl -n "${ns}" get pods -l arm -o jsonpath='{range .items[?(@.status.phase=="Running")]}{.metadata.labels.arm}{"\n"}{end}{range .items[?(@.status.phase=="Pending")]}{.metadata.labels.arm}{"\n"}{end}' 2>/dev/null | grep -v "^${model_deploy}$" | grep -v '^$' || true)"
if [[ -n "${live}" ]]; then
  echo "refusing to run: another arm is up (${live//$'\n'/ })" >&2
  exit 1
fi
echo "ok: only ${model_deploy} may be resident"

# --------------------------------------------------------------------------- #
# 2. namespace, PVCs, quota, model, page server
# --------------------------------------------------------------------------- #
step "applying namespace, model and page server"
kubectl apply -f manifests/namespace.yaml
kubectl apply -f manifests/model.yaml
kubectl apply -f manifests/page-server.yaml

step "creating the ConfigMaps the Job mounts"
kubectl -n "${ns}" create configmap pages \
  --from-file=calibration.html=harness/page/calibration.html \
  --from-file=calibration-plain.html=harness/page/calibration-plain.html \
  --from-file=targets.html=harness/page/targets.html \
  --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl -n "${ns}" create configmap e6-controller \
  --from-file=controller.py=controller/controller.py \
  --from-file=console.py=controller/console.py \
  --from-file=events.py=controller/events.py \
  --from-file=transform.py=controller/transform.py \
  --from-file=vlm.py=controller/vlm.py \
  --from-file=calibrate.py=controller/calibrate.py \
  --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl -n "${ns}" create configmap e6-fit \
  --from-file=fit.json=calibration/fit.json \
  --dry-run=client -o yaml | kubectl apply -f - >/dev/null
echo "ok: pages, e6-controller, e6-fit"

# --------------------------------------------------------------------------- #
# 3. fixtures onto the claim (frames included; a ConfigMap cannot carry them)
# --------------------------------------------------------------------------- #
loader=e6-fixtures-loader
step "copying tasks/ onto the e6-fixtures claim"
kubectl -n "${ns}" delete pod "${loader}" --ignore-not-found >/dev/null 2>&1 || true
kubectl -n "${ns}" run "${loader}" --image=busybox:1.36 --restart=Never \
  --overrides='{"spec":{"containers":[{"name":"loader","image":"busybox:1.36","command":["sleep","900"],
    "resources":{"requests":{"cpu":"50m","memory":"64Mi"},"limits":{"cpu":"200m","memory":"128Mi"}},
    "volumeMounts":[{"name":"fixtures","mountPath":"/fixtures"},{"name":"results","mountPath":"/results"}]}],
    "volumes":[{"name":"fixtures","persistentVolumeClaim":{"claimName":"e6-fixtures"}},
               {"name":"results","persistentVolumeClaim":{"claimName":"e6-results"}}]}}' >/dev/null
kubectl -n "${ns}" wait --for=condition=Ready "pod/${loader}" --timeout=180s >/dev/null
kubectl -n "${ns}" exec "${loader}" -- sh -c 'mkdir -p /fixtures && rm -rf /fixtures/* /results/* 2>/dev/null || true'
kubectl -n "${ns}" cp tasks/. "${loader}":/fixtures/
kubectl -n "${ns}" exec "${loader}" -- find /fixtures -maxdepth 2 -type f | sort

# --------------------------------------------------------------------------- #
# 4. the model
# --------------------------------------------------------------------------- #
step "scaling ${model_deploy} to 1"
kubectl -n "${ns}" scale "deploy/${model_deploy}" --replicas=1 >/dev/null
kubectl -n "${ns}" rollout status "deploy/${model_deploy}" --timeout=900s
kubectl -n "${ns}" logs "deploy/${model_deploy}" -c fetch-weights --tail=3 || true

cleanup() {
  step "scaling ${model_deploy} back to 0"
  kubectl -n "${ns}" scale "deploy/${model_deploy}" --replicas=0 >/dev/null 2>&1 || true
  kubectl -n "${ns}" wait --for=delete pod -l "arm=${model_deploy}" --timeout=240s >/dev/null 2>&1 || true
  kubectl -n "${ns}" delete pod "${loader}" --ignore-not-found >/dev/null 2>&1 || true
  rm -f -- "${spec:-}"
  kubectl -n "${ns}" get pods --no-headers || true
}

if [[ "${calibrate}" -eq 1 ]]; then
  # ------------------------------------------------------------------------- #
  # 5a. calibration only: refit the coordinate transform against a known grid
  # ------------------------------------------------------------------------- #
  step "running the calibration Job"
  kubectl -n "${ns}" delete job e6-calibrate --ignore-not-found >/dev/null
  kubectl -n "${ns}" create -f - >/dev/null <<'YAML'
apiVersion: batch/v1
kind: Job
metadata:
  name: e6-calibrate
  namespace: vlm-e6
  labels: {app.kubernetes.io/part-of: vlm-e6}
spec:
  backoffLimit: 0
  ttlSecondsAfterFinished: 86400
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: calibrate
          image: python:3.12-slim
          command: ["python3", "/opt/e6/controller/calibrate.py"]
          args:
            - "--frame"
            - "/opt/e6/tasks/calibration/frame-plain-1280x600.png"
            - "--out"
            - "/results/fit.json"
            - "--reps"
            - "3"
          env:
            - {name: MODEL_URL, value: "http://qwen3vl:8000"}
            - {name: MAX_TOKENS, value: "320"}
            - {name: TIMEOUT, value: "600"}
          resources:
            requests: {cpu: 100m, memory: 128Mi}
            limits: {cpu: "1", memory: 512Mi}
          volumeMounts:
            - {name: controller, mountPath: /opt/e6/controller, readOnly: true}
            - {name: tasks, mountPath: /opt/e6/tasks, readOnly: true}
            - {name: results, mountPath: /results}
      volumes:
        - name: controller
          configMap: {name: e6-controller}
        - name: tasks
          persistentVolumeClaim: {claimName: e6-fixtures, readOnly: true}
        - name: results
          persistentVolumeClaim: {claimName: e6-results}
YAML
  kubectl -n "${ns}" delete pod e6-calibrate-runner --ignore-not-found >/dev/null 2>&1 || true
  kubectl -n "${ns}" wait --for=condition=complete job/e6-calibrate --timeout=1800s
  kubectl -n "${ns}" logs job/e6-calibrate | tail -30
  kubectl -n "${ns}" cp "${loader}":/results/fit.json calibration/fit.json 2>/dev/null || {
    kubectl -n "${ns}" run e6-reader --image=busybox:1.36 --restart=Never \
      --overrides='{"spec":{"containers":[{"name":"r","image":"busybox:1.36","command":["sleep","600"],
        "resources":{"requests":{"cpu":"50m","memory":"64Mi"},"limits":{"cpu":"200m","memory":"128Mi"}},
        "volumeMounts":[{"name":"results","mountPath":"/results"}]}],
        "volumes":[{"name":"results","persistentVolumeClaim":{"claimName":"e6-results"}}]}}' >/dev/null
    kubectl -n "${ns}" wait --for=condition=Ready pod/e6-reader --timeout=180s >/dev/null
    kubectl -n "${ns}" cp e6-reader:/results/fit.json calibration/fit.json
    kubectl -n "${ns}" delete pod e6-reader --ignore-not-found >/dev/null 2>&1 || true
  }
  step "calibration fit"
  cat calibration/fit.json
  trap cleanup EXIT
  exit 0
fi

# --------------------------------------------------------------------------- #
# 5b. the controller
# --------------------------------------------------------------------------- #
trap cleanup EXIT

job=e6-controller
step "running ${job}: sets=${sets} arm=${arm} dry_run=${dry_run}"
kubectl -n "${ns}" delete job "${job}" --ignore-not-found >/dev/null

# One manifest, two renderings: the dry run appends --dry-run to the arg list, so
# the Job that runs is the Job that was reviewed. `cleanup` deletes this file; a
# second EXIT trap here would replace the one that scales the model back to 0.
spec="$(mktemp --suffix=.yaml)"
if [[ "${dry_run}" -eq 1 ]]; then
  sed -e "s|SETS_PLACEHOLDER|${sets}|g" -e "s|ARM_PLACEHOLDER|${arm}|g" -e "s|PAGE_URL_PLACEHOLDER|${page_url}|g" \
      -e 's|            - "/results"|            - "/results"\n            - "--dry-run"|' \
      manifests/controller-job.yaml >"${spec}"
else
  sed -e "s|SETS_PLACEHOLDER|${sets}|g" -e "s|ARM_PLACEHOLDER|${arm}|g" -e "s|PAGE_URL_PLACEHOLDER|${page_url}|g" \
    manifests/controller-job.yaml >"${spec}"
fi
kubectl -n "${ns}" create -f "${spec}" >/dev/null

step "streaming ${job} logs"
kubectl -n "${ns}" logs -f "job/${job}" || true

set +e
kubectl -n "${ns}" wait --for=condition=complete "job/${job}" --timeout=3600s
status=$?
set -e

step "collecting events.jsonl and the run's log"
mkdir -p results
kubectl -n "${ns}" logs "job/${job}" >"results/last-run.log" 2>&1 || true
kubectl -n "${ns}" delete pod e6-reader --ignore-not-found >/dev/null 2>&1 || true
kubectl -n "${ns}" run e6-reader --image=busybox:1.36 --restart=Never \
  --overrides='{"spec":{"containers":[{"name":"r","image":"busybox:1.36","command":["sleep","600"],
    "resources":{"requests":{"cpu":"50m","memory":"64Mi"},"limits":{"cpu":"200m","memory":"128Mi"}},
    "volumeMounts":[{"name":"results","mountPath":"/results"}]}],
    "volumes":[{"name":"results","persistentVolumeClaim":{"claimName":"e6-results"}}]}}' >/dev/null
kubectl -n "${ns}" wait --for=condition=Ready pod/e6-reader --timeout=180s >/dev/null
kubectl -n "${ns}" cp e6-reader:/results/. results/
kubectl -n "${ns}" delete pod e6-reader --ignore-not-found >/dev/null 2>&1 || true

step "where the run landed"
find results -name 'events.jsonl' -newermt '-2 hours' | sort

exit "${status}"
