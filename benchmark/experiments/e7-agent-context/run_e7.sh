#!/usr/bin/env bash
# E7 runner: one arm at a time, the model resident only while its arm runs, and
# teardown on every exit path.
#
# The runtime under test (apps/agent-runtime) is a host process here, reached over
# port-forwards. Building it into an image is a separate step; the experiment is
# measuring the agent, and the port-forward is the shortest honest path to a number.
#
# Usage:
#   bash run_e7.sh --dry-run                 # decide for every task, dispatch nothing
#   bash run_e7.sh --arm A2 --concurrency 1
#   bash run_e7.sh                           # A1, A2, A3 at 1, 5 and 10
#
# A3 needs Laya reachable; the script refuses the arm instead of silently running a
# plan-only comparison under the picker's name.
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo=$(cd "${here}/../../.." && pwd)
ns=agent-e7
console_url=${CONSOLE_URL:-http://192.168.0.250:8080}

arms=(A1 A2 A3)
levels=(1 5 10)
dry_run=0
only_arm=""

usage() {
  sed -n '2,20p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
}

while [ $# -gt 0 ]; do
  case "$1" in
    --dry-run) dry_run=1; shift ;;
    --arm) only_arm="${2:?--arm needs a value}"; shift 2 ;;
    --concurrency) levels=("${2:?--concurrency needs a value}"); shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "run_e7: unknown flag $1" >&2; exit 2 ;;
  esac
done

if [ -n "${only_arm}" ]; then
  arms=("${only_arm}")
fi

run_id=$(date -u +%Y%m%dT%H%M%SZ)
out="${here}/results/e7/${run_id}"
mkdir -p "${out}"
runtime_pid=""
forward_pids=()

cleanup() {
  local pid
  for pid in "${forward_pids[@]:-}"; do
    [ -n "${pid}" ] && kill "${pid}" 2>/dev/null || true
  done
  if [ -n "${runtime_pid}" ]; then
    kill "${runtime_pid}" 2>/dev/null || true
    wait "${runtime_pid}" 2>/dev/null || true
  fi
  kubectl -n "${ns}" scale deploy/qwen3vl --replicas=0 >/dev/null 2>&1 || true
  echo "run_e7: teardown done (model scaled to 0, port-forwards closed)"
}
# One trap. A second `trap ... EXIT` would replace this one and leave the single
# render device held by a pod nobody is watching — E6 recorded that mistake.
trap cleanup EXIT

echo "run_e7: run ${run_id} -> ${out}"
kubectl apply -f "${here}/manifests/namespace.yaml" >/dev/null
kubectl apply -f "${here}/manifests/page-server.yaml" >/dev/null
kubectl apply -f "${here}/manifests/model.yaml" >/dev/null
kubectl -n "${ns}" rollout status deploy/page-server --timeout=180s >/dev/null

page_ip=$(kubectl -n "${ns}" get svc page-server -o jsonpath='{.spec.clusterIP}')
page_url="http://${page_ip}:8000/targets.html"
echo "run_e7: fixture page at ${page_url}"

kubectl -n "${ns}" scale deploy/qwen3vl --replicas=1 >/dev/null
kubectl -n "${ns}" rollout status deploy/qwen3vl --timeout=900s >/dev/null

kubectl -n agent-sandbox-system port-forward svc/sandbox-router-svc 18080:8080 >/dev/null 2>&1 &
forward_pids+=("$!")
kubectl -n "${ns}" port-forward svc/qwen3vl 18000:8000 >/dev/null 2>&1 &
forward_pids+=("$!")
sleep 2

laya_url=""
if kubectl -n models get svc laya >/dev/null 2>&1; then
  kubectl -n models port-forward svc/laya 18001:8000 >/dev/null 2>&1 &
  forward_pids+=("$!")
  laya_url="http://127.0.0.1:18001/v1/systemone"
fi

arm_env() {
  case "$1" in
    A1) echo "AGENT_MAX_OPS_PER_CALL=1 AGENT_ID=qwen" ;;
    A2) echo "AGENT_MAX_OPS_PER_CALL=5 AGENT_ID=qwen" ;;
    A3) echo "AGENT_MAX_OPS_PER_CALL=5 AGENT_ID=laya+qwen" ;;
    *) echo "run_e7: unknown arm $1" >&2; return 1 ;;
  esac
}

start_runtime() {
  local arm="$1" level="$2" actions
  actions="click,type,scroll,navigate"
  if [ "${dry_run}" -eq 1 ]; then
    actions=""   # the gate then refuses every operation: inspect and decide only
  fi
  # shellcheck disable=SC2046
  (
    cd "${repo}/apps/agent-runtime"
    env $(arm_env "${arm}") \
      ADDR=:8080 \
      CONSOLE_URL="${console_url}" \
      ROUTER_URL="http://127.0.0.1:18080" \
      QWEN_URL="http://127.0.0.1:18000/v1/chat/completions" \
      LAYA_URL="${laya_url}" \
      AGENT_ACTIONS="${actions}" \
      AGENT_DOMAINS="*" \
      AGENT_DENIED="delete" \
      AGENT_START_URL="${page_url}" \
      AGENT_CONCURRENT_AGENTS="${level}" \
      AGENT_CONCURRENT_SESSIONS="${level}" \
      go run ./cmd/agent-runtime
  ) >>"${out}/runtime.log" 2>&1 &
  runtime_pid=$!
  local waited=0
  while [ "${waited}" -lt 120 ]; do
    if curl -fsS http://127.0.0.1:8080/healthz >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
    waited=$((waited + 1))
  done
  echo "run_e7: the runtime never became healthy; see ${out}/runtime.log" >&2
  return 1
}

stop_runtime() {
  if [ -n "${runtime_pid}" ]; then
    kill "${runtime_pid}" 2>/dev/null || true
    wait "${runtime_pid}" 2>/dev/null || true
    runtime_pid=""
  fi
}

# run_task posts one objective and waits for a terminal state, then records the
# pair in plan.json. Polling rather than a webhook because a run is seconds long and
# a lost callback would leave the task unaccounted for.
run_task() {
  local arm="$1" level="$2" task_json="$3"
  local task objective target response run task_id state body tries
  task_id=$(python3 -c 'import json,sys;print(json.loads(sys.argv[1])["id"])' "${task_json}")
  objective=$(python3 -c 'import json,sys;print(json.loads(sys.argv[1])["objective"])' "${task_json}")
  target=$(python3 -c 'import json,sys;print(json.loads(sys.argv[1]).get("target",""))' "${task_json}")

  body=$(python3 -c '
import json, sys
print(json.dumps({"objective": sys.argv[1], "target": sys.argv[2],
                  "agent_id": sys.argv[3], "url": sys.argv[4]}))
' "${objective}" "${target}" "${4}" "${page_url}")

  response=$(curl -fsS -X POST http://127.0.0.1:8080/agents \
    -H 'content-type: application/json' -d "${body}")
  run=$(python3 -c 'import json,sys;print(json.loads(sys.argv[1])["run_id"])' "${response}")

  tries=0
  while [ "${tries}" -lt 240 ]; do
    body=$(curl -fsS "http://127.0.0.1:8080/agents/${run}" || echo '{}')
    state=$(python3 -c 'import json,sys;print(json.loads(sys.argv[1]).get("state","?"))' "${body}")
    case "${state}" in
      running) sleep 1; tries=$((tries + 1)) ;;
      *) break ;;
    esac
  done

  python3 - "$out/plan.json" "${run}" "${task_id}" "${arm}" "${level}" "${state}" <<'PY'
import json, os, sys
path, run_id, task, arm, level, state = sys.argv[1:7]
plan = {"tasks": []}
if os.path.exists(path):
    plan = json.load(open(path, encoding="utf-8"))
plan["tasks"].append({"run_id": run_id, "task": task, "arm": arm,
                      "concurrency": int(level), "state": state})
json.dump(plan, open(path, "w", encoding="utf-8"), indent=2)
PY
  echo "run_e7: ${arm} c=${level} ${task_id} -> ${run} ${state}"
}

tasks_json=$(python3 -c '
import json, sys
print(json.dumps(json.load(open(sys.argv[1], encoding="utf-8"))))
' "${here}/tasks/tasks.json")
task_count=$(python3 -c 'import json,sys;print(len(json.loads(sys.argv[1])))' "${tasks_json}")

for arm in "${arms[@]}"; do
  if [ "${arm}" = "A3" ] && [ -z "${laya_url}" ]; then
    echo "run_e7: A3 skipped — Laya is not deployed in the models namespace" >&2
    continue
  fi
  for level in "${levels[@]}"; do
    echo "run_e7: arm ${arm}, concurrency ${level}"
    stop_runtime
    start_runtime "${arm}" "${level}"
    pids=()
    for ((i = 0; i < task_count; i++)); do
      one=$(python3 -c '
import json, sys
tasks = json.loads(sys.argv[1])
print(json.dumps(tasks[int(sys.argv[2])]))
' "${tasks_json}" "${i}")
      run_task "${arm}" "${level}" "${one}" "$(arm_env "${arm}" | sed 's/.*AGENT_ID=//')" &
      pids+=("$!")
      if [ "${#pids[@]}" -ge "${level}" ]; then
        wait "${pids[0]}" || true
        pids=("${pids[@]:1}")
      fi
    done
    for pid in "${pids[@]:-}"; do [ -n "${pid}" ] && wait "${pid}" || true; done
    stop_runtime
  done
done

echo "run_e7: scoring"
python3 "${here}/score.py" "${out}" | tee "${out}/score.txt"
echo "run_e7: done — ${out}"
