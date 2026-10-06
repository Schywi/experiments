#!/usr/bin/env bash
# Milestone 1 CLI: port-forward the assistant Service and send one message.
#
#   ./chat.sh "what is a pod?"
#
# Env: ASSISTANT_NS (default assistant), ASSISTANT_SVC (assistant), ASSISTANT_PORT (8080).
set -euo pipefail

ns="${ASSISTANT_NS:-assistant}"
svc="${ASSISTANT_SVC:-assistant}"
port="${ASSISTANT_PORT:-8080}"
message="${1:-hello}"

kubectl -n "${ns}" port-forward "svc/${svc}" "${port}:${port}" >/dev/null 2>&1 &
pf=$!
trap 'kill "${pf}" 2>/dev/null || true' EXIT

for _ in $(seq 1 20); do
  if curl -sf "http://localhost:${port}/healthz" >/dev/null 2>&1; then break; fi
  sleep 0.5
done

payload="$(python3 -c 'import json,sys; print(json.dumps({"message": sys.argv[1]}))' "${message}")"
curl -s "http://localhost:${port}/chat" -H 'content-type: application/json' --data-binary "${payload}"
echo
