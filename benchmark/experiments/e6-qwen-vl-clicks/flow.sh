#!/usr/bin/env bash
# E6 end to end in one script, with every step printed.
#
#   bash flow.sh                        # everything, vision clicks allowed, hands off at the end
#   bash flow.sh --keep                 # same, but leave the namespace up for inspection
#   bash flow.sh --only dom-unique      # one arm
#   bash flow.sh --page-url http://10.43.231.227:8000/targets.html
#   bash flow.sh --calibrate            # calibration only, then stop
#   bash flow.sh --hard                 # do not allow vision clicks (keeps the calibration gate)
#
# What it does:
#
#   1  preflight: cluster reachable, no other model arm resident
#   2  namespace, quota, model, page server, ConfigMaps
#   3  fixtures onto the e6-fixtures claim (the PNGs are too big for a ConfigMap)
#   4  model up, wait until /health answers
#   5  calibration (refits calibration/fit.json) unless --skip-calibrate
#   6  the arms: vision-only, dom-unique, dom-candidates — one Job each, logs streamed
#   7  events.jsonl copied from the e6-results claim into results/
#   8  model back to 0 (always), namespace deleted unless --keep
#
# The model is scaled to 0 on every exit path: the node has one render device, and
# leaving it held blocks the next run.
set -euo pipefail

here="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
cd -- "${here}"

ns=vlm-e6
deploy=qwen3vl
page_url="http://192.168.0.250:8080/"
only=""
keep=0
calibrate_only=0
skip_calibrate=0
allow_vision=1

while [[ $# -gt 0 ]]; do
  case "$1" in
    --keep) keep=1 ;;
    --calibrate) calibrate_only=1 ;;
    --skip-calibrate) skip_calibrate=1 ;;
    --hard) allow_vision=0 ;;
    --only) shift; only="$1" ;;
    --page-url) shift; page_url="$1" ;;
    -h|--help) sed -n '2,31p' "$0"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
  shift
done

banner() { printf '\n========== %s\n' "$*"; }
say()    { printf '   %s\n' "$*"; }
die()    { printf 'FAILED: %s\n' "$*" >&2; exit 1; }

# --------------------------------------------------------------------------- #
banner "1/8 preflight"
kubectl version >/dev/null 2>&1 || die "kubectl cannot reach the cluster"
kubectl get node >/dev/null || die "no node visible to kubectl"
say "cluster reachable"
live="$(kubectl -n "${ns}" get pods -l arm -o jsonpath='{range .items[?(@.status.phase=="Running")]}{.metadata.labels.arm}{"\n"}{end}{range .items[?(@.status.phase=="Pending")]}{.metadata.labels.arm}{"\n"}{end}' 2>/dev/null | grep -v "^${deploy}$" | grep -v '^$' || true)"
[[ -z "${live}" ]] || die "another arm is up: ${live//$'\n'/ }"
say "only ${deploy} may hold the render device"

# --------------------------------------------------------------------------- #
banner "2/8 namespace, model, page server, ConfigMaps"
kubectl apply -f manifests/namespace.yaml
kubectl apply -f manifests/model.yaml
kubectl apply -f manifests/page-server.yaml
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
say "pages, e6-controller, e6-fit applied"

# --------------------------------------------------------------------------- #
banner "3/8 fixtures onto the e6-fixtures claim"
loader=e6-fixtures-loader
kubectl -n "${ns}" delete pod "${loader}" --ignore-not-found >/dev/null 2>&1 || true
kubectl -n "${ns}" run "${loader}" --image=busybox:1.36 --restart=Never \
  --overrides='{"spec":{"containers":[{"name":"loader","image":"busybox:1.36","command":["sleep","1800"],
    "resources":{"requests":{"cpu":"50m","memory":"64Mi"},"limits":{"cpu":"200m","memory":"128Mi"}},
    "volumeMounts":[{"name":"fixtures","mountPath":"/fixtures"},{"name":"results","mountPath":"/results"}]}],
    "volumes":[{"name":"fixtures","persistentVolumeClaim":{"claimName":"e6-fixtures"}},
               {"name":"results","persistentVolumeClaim":{"claimName":"e6-results"}}]}}' >/dev/null
kubectl -n "${ns}" wait --for=condition=Ready "pod/${loader}" --timeout=180s >/dev/null
kubectl -n "${ns}" exec "${loader}" -- sh -c 'mkdir -p /fixtures && rm -rf /fixtures/* 2>/dev/null || true'
kubectl -n "${ns}" cp tasks/. "${loader}":/fixtures/
kubectl -n "${ns}" cp calibration/. "${loader}":/fixtures/calibration/ 2>/dev/null || {
  kubectl -n "${ns}" exec "${loader}" -- mkdir -p /fixtures/calibration
  kubectl -n "${ns}" cp calibration/. "${loader}":/fixtures/calibration/
}
kubectl -n "${ns}" exec "${loader}" -- find /fixtures -maxdepth 2 -name '*.json' -o -maxdepth 2 -name '*.png' | sort

cleanup() {
  banner "8/8 teardown"
  say "scaling ${deploy} to 0"
  kubectl -n "${ns}" scale "deploy/${deploy}" --replicas=0 >/dev/null 2>&1 || true
  kubectl -n "${ns}" wait --for=delete pod -l "arm=${deploy}" --timeout=240s >/dev/null 2>&1 || true
  kubectl -n "${ns}" delete pod "${loader}" e6-reader --ignore-not-found >/dev/null 2>&1 || true
  rm -f -- "${spec:-}"
  if [[ "${keep}" -eq 1 ]]; then
    say "--keep: namespace ${ns} left up"
  else
    say "deleting namespace ${ns}"
    kubectl delete ns "${ns}" --wait=false >/dev/null 2>&1 || true
  fi
  kubectl -n "${ns}" get pods --no-headers 2>/dev/null || true
}
trap cleanup EXIT

# --------------------------------------------------------------------------- #
banner "4/8 model up"
kubectl -n "${ns}" scale "deploy/${deploy}" --replicas=1 >/dev/null
kubectl -n "${ns}" rollout status "deploy/${deploy}" --timeout=900s
kubectl -n "${ns}" logs "deploy/${deploy}" -c vlm --tail=3 || true

# --------------------------------------------------------------------------- #
run_job() {                     # name, command-args..., out-log
  local name="$1"; shift
  local logfile="$1"; shift
  banner "running ${name}"
  kubectl -n "${ns}" delete job "${name}" --ignore-not-found >/dev/null
  local spec
  spec="$(mktemp --suffix=.yaml)"
  cp "$1" "${spec}"
  kubectl -n "${ns}" create -f "${spec}" >/dev/null
  rm -f -- "${spec}"
  say "streaming logs (ctrl-c leaves the Job; the trap still scales the model to 0)"
  kubectl -n "${ns}" logs -f "job/${name}" | tee "${logfile}" || true
  kubectl -n "${ns}" wait --for=condition=complete "job/${name}" --timeout=3600s || \
    say "job ${name} did not report Complete; the log above is the evidence"
}

# --------------------------------------------------------------------------- #
banner "5/8 calibration"
if [[ "${skip_calibrate}" -eq 1 ]]; then
  say "--skip-calibrate: reusing calibration/fit.json"
else
  cat >/tmp/e6-calibrate.yaml <<'YAML'
apiVersion: batch/v1
kind: Job
metadata:
  name: e6-calibrate
  namespace: vlm-e6
spec:
  backoffLimit: 0
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: calibrate
          image: python:3.12-slim
          command: ["python3", "/opt/e6/controller/calibrate.py"]
          args: ["--frame", "/opt/e6/tasks/calibration/frame-plain-1280x600.png",
                 "--out", "/results/fit.json", "--reps", "3"]
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
  run_job e6-calibrate /tmp/e6-calibrate.log /tmp/e6-calibrate.yaml
  kubectl -n "${ns}" exec "${loader}" -- cat /results/fit.json > calibration/fit.json 2>/dev/null || true
  say "fit written back:"
  grep -o '"status": *"[a-z]*"' calibration/fit.json || true
fi

if [[ "${calibrate_only}" -eq 1 ]]; then
  say "--calibrate only: stopping here"
  exit 0
fi

# --------------------------------------------------------------------------- #
banner "6/8 the arms"
if [[ -n "${only}" ]]; then
  arms=("${only}")
else
  arms=(vision-only dom-unique dom-candidates)
fi

for arm in "${arms[@]}"; do
  case "${arm}" in
    vision-only)     sets="targets";                      vision="${allow_vision}" ;;
    dom-unique)      sets="targets";                      vision=0 ;;
    dom-candidates)  sets="discriminate,targets";         vision=0 ;;
    *) die "unknown arm: ${arm}" ;;
  esac
  spec="/tmp/e6-${arm}.yaml"
  sed -e "s|SETS_PLACEHOLDER|${sets}|g" \
      -e "s|ARM_PLACEHOLDER|${arm}|g" \
      -e "s|PAGE_URL_PLACEHOLDER|${page_url}|g" \
      -e "s|VISION_PLACEHOLDER|${vision}|g" \
    manifests/controller-job.yaml >"${spec}"
  say "arm=${arm} sets=${sets} page=${page_url} allow_vision_clicks=${vision}"
  run_job "e6-controller" "results/${arm}.log" "${spec}"
done

# --------------------------------------------------------------------------- #
banner "7/8 collecting events.jsonl"
kubectl -n "${ns}" delete pod e6-reader --ignore-not-found >/dev/null 2>&1 || true
kubectl -n "${ns}" run e6-reader --image=busybox:1.36 --restart=Never \
  --overrides='{"spec":{"containers":[{"name":"r","image":"busybox:1.36","command":["sleep","900"],
    "resources":{"requests":{"cpu":"50m","memory":"64Mi"},"limits":{"cpu":"200m","memory":"128Mi"}},
    "volumeMounts":[{"name":"results","mountPath":"/results"}]}],
    "volumes":[{"name":"results","persistentVolumeClaim":{"claimName":"e6-results"}}]}}' >/dev/null
kubectl -n "${ns}" wait --for=condition=Ready pod/e6-reader --timeout=180s >/dev/null
mkdir -p results
kubectl -n "${ns}" cp e6-reader:/results/. results/
kubectl -n "${ns}" delete pod e6-reader --ignore-not-found >/dev/null 2>&1 || true

say "runs on disk:"
find results/e6 -name events.jsonl 2>/dev/null | sort || true
say "clicks dispatched per run:"
for f in results/e6/*/events.jsonl; do
  [[ -f "$f" ]] || continue
  printf '   %s  actions=%s  rejected=%s\n' \
    "$(dirname "$f" | xargs basename)" \
    "$(grep -c '"event": "browser.action.completed"' "$f" || true)" \
    "$(grep -c '"event": "browser.action.rejected"' "$f" || true)"
done
