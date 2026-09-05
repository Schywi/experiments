#!/usr/bin/env bash

set -euo pipefail

command -v docker >/dev/null 2>&1 || {
  echo "docker is required" >&2
  exit 1
}
command -v k3d >/dev/null 2>&1 || {
  echo "k3d is required" >&2
  exit 1
}

CLUSTER_NAME="${K3D_CLUSTER_NAME:-cilium-lab}"
K3S_IMAGE="${K3D_K3S_IMAGE:-rancher/k3s:v1.30.6-k3s1}"
MEMORY_LIMIT="${K3D_MEMORY_LIMIT:-2g}"
API_PORT="${K3D_API_PORT:-6445}"
CREATE_TIMEOUT="${K3D_CREATE_TIMEOUT:-5m}"

if [[ "${MEMORY_LIMIT}" != "2g" ]]; then
  echo "K3D_MEMORY_LIMIT must remain 2g; refusing a larger cluster" >&2
  exit 1
fi

if k3d cluster list --no-headers 2>/dev/null | awk '{print $1}' | grep -Fxq "${CLUSTER_NAME}"; then
  echo "k3d cluster '${CLUSTER_NAME}' already exists"
  exit 0
fi

# One Docker-backed k3s server, no agents and no k3d load balancer. The server
# is the only cluster container and is hard-capped at 2 GiB.
k3d cluster create "${CLUSTER_NAME}" \
  --image "${K3S_IMAGE}" \
  --servers 1 \
  --agents 0 \
  --no-lb \
  --servers-memory "${MEMORY_LIMIT}" \
  --port "127.0.0.1:${API_PORT}:6443@server:0" \
  --k3s-arg "--flannel-backend=none@server:0" \
  --k3s-arg "--disable-network-policy@server:0" \
  --k3s-arg "--disable-kube-proxy@server:0" \
  --k3s-arg "--disable=traefik@server:0" \
  --k3s-arg "--disable=servicelb@server:0" \
  --timeout "${CREATE_TIMEOUT}" \
  --wait

echo "Created Docker-backed k3d cluster '${CLUSTER_NAME}' with a 2 GiB server cap"
echo "Current context: k3d-${CLUSTER_NAME}"
