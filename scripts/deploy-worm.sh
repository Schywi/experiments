#!/usr/bin/env bash

set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "${script_dir}/.." && pwd)"
cluster_name="${K3D_CLUSTER_NAME:-cilium-lab}"
namespace="${WORM_NAMESPACE:-worm-lab}"
helm_timeout="${HELM_TIMEOUT:-10m}"
image_build="${repo_root}/scripts/build-and-import-image.sh"

command -v helm >/dev/null 2>&1 || { echo "helm is required" >&2; exit 1; }
command -v kubectl >/dev/null 2>&1 || { echo "kubectl is required" >&2; exit 1; }

build_image() {
  local image_ref="$1"
  local context="$2"
  printf '\n==> build and import %s\n' "${image_ref}"
  "${image_build}" "${image_ref}" "${repo_root}/${context}"
}

deploy_chart() {
  local release="$1"
  local chart="$2"
  local deployment="$3"
  printf '\n==> deploy %s\n' "${release}"
  helm upgrade --install "${release}" "${repo_root}/${chart}" \
    --namespace "${namespace}" \
    --create-namespace \
    --set image.pullPolicy=Never \
    --wait \
    --timeout "${helm_timeout}"
  kubectl --namespace "${namespace}" rollout status "deployment/${deployment}" \
    --timeout="${helm_timeout}"
}

build_image "worm-controller:tilt" apps/controller
build_image "worm-regression:tilt" apps/regression
build_image "worm-worker:tilt" apps/worker

deploy_chart worm-controller apps/controller/chart worm-controller
deploy_chart worm-regression apps/regression/chart regression

printf '\n==> deploy worm-vector\n'
helm upgrade --install worm-vector "${repo_root}/config/vector" \
  --namespace "${namespace}" \
  --create-namespace \
  --wait \
  --timeout "${helm_timeout}"
kubectl --namespace "${namespace}" rollout status deployment/vector \
  --timeout="${helm_timeout}"

deploy_chart worm-worker apps/worker/chart worm-worker

kubectl --namespace "${namespace}" get deployments,services,pods
printf '<== Worm deployment complete in cluster %s\n' "${cluster_name}"
