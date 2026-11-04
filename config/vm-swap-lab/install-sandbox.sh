#!/usr/bin/env bash
# Install agent-sandbox and the one browser pool inside the VM.
#
# The controller comes from the same pinned upstream release and the same
# recorded digest that config/sandbox/install.sh uses, so the version under test
# is the version the lab platform runs. Nothing is taken from `latest`.
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "${script_dir}/lib.sh"

SANDBOX_VERSION="${SANDBOX_VERSION:-v1.0.7}"
# sha256 of sandbox-with-extensions.yaml as published for v1.0.7 — same value as
# config/sandbox/install.sh; bump both together.
EXPECTED_SHA256="${EXPECTED_SHA256:-136c10c9347cb5a6740b351444cae82f404e883c3f520272257ba785f2af0da8}"
RELEASE_URL="https://github.com/kubernetes-sigs/agent-sandbox/releases/download/${SANDBOX_VERSION}/sandbox-with-extensions.yaml"
# The manifest is cached by fetch-sandbox.sh, so a play needs no egress. Live
# download stays as a fallback for a fresh checkout, but with retries: this used
# to be a bare `curl` and a 90-second DNS outage killed both arms of a run.
CACHED_MANIFEST="${script_dir}/cache/sandbox-${SANDBOX_VERSION}.yaml"

workdir="$(mktemp -d)"
trap 'rm -rf -- "${workdir}"' EXIT

if [[ -s "${CACHED_MANIFEST}" ]]; then
  echo "using cached agent-sandbox ${SANDBOX_VERSION} manifest"
  cp "${CACHED_MANIFEST}" "${workdir}/sandbox.yaml"
else
  echo "downloading agent-sandbox ${SANDBOX_VERSION} (not cached; ./fetch-sandbox.sh caches it)"
  curl -fsSL --retry 5 --retry-delay 3 --retry-all-errors --connect-timeout 20 \
    -o "${workdir}/sandbox.yaml" "${RELEASE_URL}"
fi
actual="$(sha256sum "${workdir}/sandbox.yaml" | awk '{print $1}')"
if [[ "${actual}" != "${EXPECTED_SHA256}" ]]; then
  echo "sha256 mismatch for ${SANDBOX_VERSION}: expected ${EXPECTED_SHA256}, got ${actual}" >&2
  exit 1
fi
echo "manifest verified (${actual})"

scp -F /dev/null -i "${SSH_KEY}" -P "${SSH_PORT}" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
  "${workdir}/sandbox.yaml" "${SSH_USER}@${VM_HOST}:/tmp/sandbox.yaml"
scp -F /dev/null -i "${SSH_KEY}" -P "${SSH_PORT}" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR \
  "${script_dir}/manifests.yaml" "${SSH_USER}@${VM_HOST}:/tmp/manifests.yaml"

echo "applying CRDs, controller, namespace, template and pool"
vm_ssh 'sudo k3s kubectl apply -f /tmp/sandbox.yaml'
vm_ssh "sudo k3s kubectl -n agent-sandbox-system rollout status deploy/agent-sandbox-controller --timeout=300s"
vm_ssh 'sudo k3s kubectl apply -f /tmp/manifests.yaml'

echo "waiting for the warm pool to fill"
replicas="$(vm_ssh "sudo k3s kubectl -n sandbox-demo get sandboxwarmpool browser-pool -o jsonpath='{.spec.replicas}'")"
filled=0
for _ in $(seq 1 90); do
  ready="$(vm_ssh "sudo k3s kubectl -n sandbox-demo get sandboxwarmpool browser-pool -o jsonpath='{.status.readyReplicas}'" || true)"
  ready="${ready:-0}"
  echo "  ready ${ready}/${replicas}"
  if [[ "${ready}" -ge "${replicas}" ]]; then
    filled=1
    break
  fi
  sleep 5
done

# Fail here rather than 3 tasks later in the ladder. A pool that never fills is
# almost always an image or scheduling problem, and it is knowable right now — the
# previous version just printed the pool and carried on, so the ladder reported a
# bare "0/n" with no cause attached.
if [[ "${filled}" -ne 1 ]]; then
  echo "FAIL: warm pool did not fill (${ready}/${replicas})" >&2
  vm_ssh 'sudo k3s kubectl -n sandbox-demo get pods -o wide' >&2 || true
  vm_ssh 'sudo k3s kubectl -n sandbox-demo describe pods' 2>&1 | grep -A3 -iE "events:|failed|error|warning" | tail -25 >&2 || true
  exit 1
fi

vm_ssh 'sudo k3s kubectl -n sandbox-demo get sandboxwarmpool,pods -o wide'
echo
echo "next: ./capacity.sh (the playbook runs it with the arm's class)"
