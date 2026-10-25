#!/usr/bin/env bash
# Chromium for a sandbox tile, plus the HTTP shim the router can actually reach.
#
# Three facts decide this script, each verified on this cluster:
#
#   1. Chromium 130 ignores --remote-debugging-address and binds DevTools to
#      127.0.0.1. Loopback answers; the pod IP refuses from inside the pod's own
#      network namespace, in both --headless=new and --headless=old.
#   2. The sandbox-router cannot forward a WebSocket in v1.0.7 or v1.0.8:
#      proxy.go:545 wraps every upstream body in a counting wrapper, so Go's
#      ReverseProxy aborts the 101 with "non-writable body". HTTP through the
#      router works; upgrades do not.
#   3. --no-sandbox is required: the container cannot give Chromium the
#      namespaces its own sandbox wants (experiment 02, Q2). The NetworkPolicy
#      is the boundary, not Chromium's sandbox.
#
# So: Chromium keeps its loopback listener, cdp-bridge.py publishes plain HTTP on
# the pod IP, and the console speaks HTTP through the router. The readiness probe
# hits the same port and now exercises the whole shim.
set -euo pipefail

# What the router, the console and the readiness probe use.
CDP_PORT="${CDP_PORT:-9222}"
# Where Chromium really listens (loopback only).
CDP_UPSTREAM_PORT="${CDP_UPSTREAM_PORT:-9223}"
TARGET_URL="${TARGET_URL:-https://example.com/}"
WINDOW_SIZE="${WINDOW_SIZE:-1280,600}"
BROWSERS_PATH="${PLAYWRIGHT_BROWSERS_PATH:-/ms-playwright}"

chrome_bin() {
  local candidate
  candidate="$(find "${BROWSERS_PATH}" -maxdepth 4 -type f -name chrome 2>/dev/null | head -n 1)"
  if [[ -z "${candidate}" ]]; then
    echo "no chromium under ${BROWSERS_PATH}; is this the Playwright image?" >&2
    exit 1
  fi
  printf '%s\n' "${candidate}"
}

CHROME="$(chrome_bin)"
echo "starting ${CHROME} on 127.0.0.1:${CDP_UPSTREAM_PORT} for ${TARGET_URL}"

"${CHROME}" \
  --headless=new \
  --no-sandbox \
  --disable-gpu \
  --disable-dev-shm-usage \
  --disable-background-timer-throttling \
  --disable-renderer-backgrounding \
  --disable-features=Vulkan,VulkanFromANGLE \
  --hide-scrollbars \
  --mute-audio \
  --no-first-run \
  --no-default-browser-check \
  --user-data-dir=/tmp/chrome-data \
  --window-size="${WINDOW_SIZE}" \
  --remote-debugging-port="${CDP_UPSTREAM_PORT}" \
  "${TARGET_URL}" &

CHROME_PID=$!

CDP_PORT="${CDP_PORT}" CDP_UPSTREAM_PORT="${CDP_UPSTREAM_PORT}" \
  python3 /usr/local/bin/cdp-bridge &

BRIDGE_PID=$!

# Optional: a raw TCP publisher for the WebSocket transport, used only if the
# console runs with CDP_TRANSPORT=ws (e.g. once upstream fixes the 101 wrap).
if [[ "${CDP_WS_PORT:-0}" != "0" ]]; then
  CDP_PORT="${CDP_WS_PORT}" CDP_UPSTREAM_PORT="${CDP_UPSTREAM_PORT}" \
    python3 /usr/local/bin/cdp-forward &
  FORWARD_PID=$!
else
  FORWARD_PID=""
fi

cleanup() {
  kill -TERM "${CHROME_PID}" "${BRIDGE_PID}" ${FORWARD_PID:+"${FORWARD_PID}"} 2>/dev/null || true
}
trap cleanup TERM INT

# Hold the pod open for the life of the browser. The SandboxTemplate readiness
# probe is what reports CDP as up, so this script never lies about it.
wait "${CHROME_PID}"
