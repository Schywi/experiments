#!/usr/bin/env bash
# Install the agent-sandbox platform this repo's console talks to.
#
# Two steps, in this order:
#   1. the pinned upstream release manifest (four CRDs + the controller);
#   2. this chart's overlay (router, browser SandboxTemplate, warm pool).
#
# The upstream manifests are downloaded from the pinned release and verified
# against a recorded digest. Nothing is built from, or written to, the
# agent-sandbox checkout.
#
# Usage:  config/sandbox/install.sh [--check]
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "${script_dir}/../.." && pwd)"

SANDBOX_VERSION="${SANDBOX_VERSION:-v1.0.7}"
# sha256 of sandbox-with-extensions.yaml as published for v1.0.7. Recompute and
# record a new value when bumping SANDBOX_VERSION.
DEFAULT_VERSION="v1.0.7"
DEFAULT_SHA256="136c10c9347cb5a6740b351444cae82f404e883c3f520272257ba785f2af0da8"
UPSTREAM_SHA256="${UPSTREAM_SHA256:-${DEFAULT_SHA256}}"
RELEASE_URL="https://github.com/kubernetes-sigs/agent-sandbox/releases/download/${SANDBOX_VERSION}/sandbox-with-extensions.yaml"

check_only=0
for arg in "$@"; do
  case "${arg}" in
    --check) check_only=1 ;;
    *) echo "unknown argument: ${arg}" >&2; exit 2 ;;
  esac
done

if [[ "${SANDBOX_VERSION}" != "${DEFAULT_VERSION}" && "${UPSTREAM_SHA256}" == "${DEFAULT_SHA256}" ]]; then
  echo "SANDBOX_VERSION=${SANDBOX_VERSION} needs its own UPSTREAM_SHA256; refusing to apply an unverified manifest" >&2
  exit 2
fi

workdir="$(mktemp -d)"
trap 'rm -rf -- "${workdir}"' EXIT

echo "Downloading agent-sandbox ${SANDBOX_VERSION}"
curl -fsSL -o "${workdir}/sandbox.yaml" "${RELEASE_URL}"

echo "Verifying ${UPSTREAM_SHA256}"
actual="$(sha256sum "${workdir}/sandbox.yaml" | awk '{print $1}')"
if [[ "${actual}" != "${UPSTREAM_SHA256}" ]]; then
  echo "sha256 mismatch: expected ${UPSTREAM_SHA256}, got ${actual}" >&2
  exit 1
fi

if ((check_only)); then
  echo "Manifest verified; nothing applied (--check)."
  exit 0
fi

echo "Applying CRDs and the controller"
kubectl apply -f "${workdir}/sandbox.yaml"
kubectl -n agent-sandbox-system rollout status deploy/agent-sandbox-controller --timeout=180s

echo "Applying the overlay (router, template, warm pool)"
helm upgrade --install sandbox "${repo_root}/config/sandbox" \
  --namespace agent-sandbox-system \
  --create-namespace \
  --wait --timeout 180s

echo
echo "Next: build and push the browser image, then check the tile path:"
echo "  buildah bud -t browser-sandbox:local ${repo_root}/apps/browser-sandbox"
echo "  ${repo_root}/config/registry/push-image.sh browser-sandbox"
# The address printed below is stale. 192.168.0.248 belonged to the router
# before 2026-10-24; the kargo-gateway LoadBalancers took .248 and .249 that
# day, the console took .250, and the router became a ClusterIP
# (sandbox-router-svc.agent-sandbox-system:8080, 10.43.71.0). Following this
# curl now reaches Kargo's gateway. Port-forward the Service instead:
#   kubectl -n agent-sandbox-system port-forward svc/sandbox-router-svc 8080:8080
echo "  curl -H 'X-Sandbox-ID: <claimed-sandbox>' -H 'X-Sandbox-Port: 9222' \\"
echo "       -H \"Authorization: Bearer \$(kubectl create token sandbox-console -n sandbox-console)\" \\"
echo "       http://192.168.0.248/json/version"
