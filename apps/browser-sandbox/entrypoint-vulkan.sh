#!/usr/bin/env bash
# The same Chromium, rasterising on the host's AMD card through Vulkan.
#
# The differences from entrypoint.sh are three, and they are the whole point of
# this file: the GPU flags replace --disable-gpu, the RADV ICD is named
# explicitly, and mesa's Vulkan drivers are in the image (Containerfile.vulkan).
# Everything else — DevTools on loopback, the HTTP bridge, --no-sandbox, the
# readiness answer — is identical, so a session behaves the same and only the
# raster path changes.
set -euo pipefail

CDP_PORT="${CDP_PORT:-9222}"
CDP_UPSTREAM_PORT="${CDP_UPSTREAM_PORT:-9223}"
TARGET_URL="${TARGET_URL:-https://example.com/}"
WINDOW_SIZE="${WINDOW_SIZE:-1280,600}"
BROWSERS_PATH="${PLAYWRIGHT_BROWSERS_PATH:-/ms-playwright}"
# Mesa's RADV ICD for this host's card (Polaris10, gfx803). Set in the image, and
# overridable so the same image can run on another render node.
VK_ICD_FILENAMES="${VK_ICD_FILENAMES:-/usr/share/vulkan/icd.d/radeon_icd.x86_64.json}"
export VK_ICD_FILENAMES

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
echo "starting ${CHROME} on 127.0.0.1:${CDP_UPSTREAM_PORT} for ${TARGET_URL} with ${VK_ICD_FILENAMES}"

"${CHROME}" \
  --headless=new \
  --no-sandbox \
  --use-gl=angle \
  --use-angle=vulkan \
  --use-vulkan=native \
  --disable-vulkan-surface \
  --enable-features=Vulkan,VulkanFromANGLE \
  --disable-dev-shm-usage \
  --disable-background-timer-throttling \
  --disable-renderer-backgrounding \
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

if [[ "${CDP_WS_PORT:-0}" != "0" ]]; then
  CDP_PORT="${CDP_WS_PORT}" CDP_UPSTREAM_PORT="${CDP_UPSTREAM_PORT}" \
    python3 /usr/local/bin/cdp-forward &
  FORWARD_PID=$?
else
  FORWARD_PID=""
fi

cleanup() {
  kill -TERM "${CHROME_PID}" "${BRIDGE_PID}" ${FORWARD_PID:+"${FORWARD_PID}"} 2>/dev/null || true
}
trap cleanup TERM INT

wait "${CHROME_PID}"
