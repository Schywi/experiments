#!/usr/bin/env bash
# The rendered chart must probe both CDP paths.
#
# The browser container's probe covers 9222 (the in-pod bridge). A run configured
# with --cdp-transport ws dials 9224 (the cdp-forward sidecar), and until this
# check existed nothing did. Run B's first failure was a ready pod whose 9224 was
# refused: 1378 sessions lost to one instance.
#
#   bash config/sandbox/tests/probes.sh
#
# Exits non-zero when a template renders without a probe on either port, so a
# chart edit that drops one is caught before it reaches a cluster.
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
chart_dir="${script_dir}/.."
helm_bin="${HELM_BIN:-helm}"

rendered="$("${helm_bin}" template sandbox "${chart_dir}" -n agent-sandbox-system)"

# Split the render per container block and count the probes.
bridge_probes="$(printf '%s' "${rendered}" | grep -c 'port: cdp$' || true)"
forward_probes="$(printf '%s' "${rendered}" | grep -c 'port: cdp-ws' || true)"
templates="$(printf '%s' "${rendered}" | grep -c '^kind: SandboxTemplate' || true)"

status=0
if (( templates == 0 )); then
  echo "no SandboxTemplate rendered" >&2
  exit 1
fi
if (( bridge_probes < templates )); then
  echo "bridge probe missing: ${bridge_probes} probes on port cdp for ${templates} template(s)" >&2
  status=1
fi
if (( forward_probes < templates )); then
  echo "forwarder probe missing: ${forward_probes} probes on port cdp-ws for ${templates} template(s)" >&2
  status=1
fi

if (( status == 0 )); then
  echo "probes: ${templates} template(s), ${bridge_probes} on cdp (9222), ${forward_probes} on cdp-ws (9224)"
fi
exit "${status}"
