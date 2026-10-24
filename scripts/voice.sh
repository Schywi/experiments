#!/usr/bin/env bash
# Milestone 8 host voice client. Keeps voice OUT of the server: this script only
# shuttles audio <-> the assistant's text endpoints.
#
#   scripts/voice.sh listen              # record, transcribe (if STT set), ask, speak
#   scripts/voice.sh say "what is up?"   # text -> /investigate -> play the WAV
#
# Requires: arecord + aplay (alsa-utils) for audio; curl. STT is optional — set
# STT_URL to a local OpenAI-compatible transcription service; without it,
# `listen` fails clearly and `say` still works.
set -euo pipefail

ns="${ASSISTANT_NS:-assistant}"
svc="${ASSISTANT_SVC:-assistant}"
port="${ASSISTANT_PORT:-8080}"
endpoint="${ASSISTANT_ENDPOINT:-investigate}"

pf_pid=""
cleanup() { [ -n "${pf_pid}" ] && kill "${pf_pid}" 2>/dev/null || true; }
trap cleanup EXIT

kubectl -n "${ns}" port-forward "svc/${svc}" "${port}:${port}" >/dev/null 2>&1 &
pf_pid=$!
for _ in $(seq 1 20); do
  curl -sf "http://localhost:${port}/healthz" >/dev/null 2>&1 && break
  sleep 0.5
done

api="http://localhost:${port}"

ask_and_speak() {
  local text="$1"
  local body reply wav
  body="$(printf '{"message": %s}' "$(printf '%s' "${text}" | python3 -c 'import json,sys;print(json.dumps(sys.stdin.read()))')")"
  reply="$(curl -s "${api}/${endpoint}" -H 'content-type: application/json' --data-binary "${body}" \
            | python3 -c 'import json,sys;print(json.load(sys.stdin).get("reply",""))')"
  echo "assistant: ${reply}"
  wav="$(mktemp --suffix=.wav)"
  printf '{"message": %s}' "$(printf '%s' "${reply}" | python3 -c 'import json,sys;print(json.dumps(sys.stdin.read()))')" \
    | curl -s "${api}/speak" -H 'content-type: application/json' --data-binary @- --output "${wav}"
  aplay -q "${wav}" && rm -f "${wav}"
}

case "${1:-}" in
  say)
    shift; ask_and_speak "$*" ;;
  listen)
    command -v arecord >/dev/null || { echo "arecord not found (install alsa-utils)" >&2; exit 1; }
    [ -n "${STT_URL:-}" ] || { echo "set STT_URL to a local STT service for 'listen'" >&2; exit 1; }
    raw="$(mktemp --suffix=.wav)"
    echo "listening (5s)..."
    arecord -q -f cd -d 5 "${raw}"
    text="$(curl -s "${STT_URL%/}/v1/audio/transcriptions" -F "file=@${raw}" \
             | python3 -c 'import json,sys;print(json.load(sys.stdin).get("text",""))')"
    rm -f "${raw}"
    echo "you: ${text}"
    [ -n "${text}" ] && ask_and_speak "${text}" ;;
  *)
    echo "usage: voice.sh {listen|say <text>}" >&2; exit 2 ;;
esac
