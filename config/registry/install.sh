#!/usr/bin/env bash
#
# One-time, ROOTLESS setup for the local image registry.
#
#   * applies the in-cluster registry (namespace `registry`)
#   * drops a user-scoped registries.conf so buildah/podman/skopeo treat
#     127.0.0.1:5000 as plain-HTTP
#
# No sudo anywhere. Point KUBECONFIG at the cluster first, e.g.
#   export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
set -euo pipefail

here="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

command -v kubectl >/dev/null 2>&1 || { echo "kubectl is required" >&2; exit 1; }

echo "==> apply the in-cluster registry"
kubectl apply -f "${here}/registry.yaml"

echo "==> wait for the registry to become Ready"
kubectl -n registry rollout status deployment/local-registry --timeout=120s

echo "==> install user registries.conf (plain-HTTP for 127.0.0.1:5000)"
dest_dir="${XDG_CONFIG_HOME:-${HOME}/.config}/containers/registries.conf.d"
mkdir -p "${dest_dir}"
install -m 0644 "${here}/registries.conf" "${dest_dir}/local-registry.conf"

echo "==> done: registry is listening on 127.0.0.1:5000 for buildah and containerd"
