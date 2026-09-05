#!/usr/bin/env bash

set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "${script_dir}/../.." && pwd)"

"${script_dir}/create.sh"

bootstrap_status=0
if ! "${script_dir}/import-images.sh"; then
  echo "WARNING: local image import failed; continuing independent platform branches" >&2
  bootstrap_status=1
fi

# Cilium, OpenResty, and Argo CD are sibling branches after k3d creation. Keep
# the Cilium Helm wait from blocking the other branches when the node cannot
# yet pull or run Cilium images.
"${script_dir}/install-cilium.sh" &
cilium_pid=$!

helm upgrade --install cilium-dashboard "${repo_root}/config/openresty" \
  --namespace openresty \
  --create-namespace \
  --wait \
  --timeout "${HELM_TIMEOUT:-10m}" &
openresty_pid=$!

"${repo_root}/config/argocd/install.sh" &
argocd_pid=$!

if ! wait "${openresty_pid}"; then
  echo "WARNING: OpenResty bootstrap failed; inspect its Helm/pod events" >&2
  bootstrap_status=1
fi

if ! wait "${argocd_pid}"; then
  echo "WARNING: Argo CD bootstrap failed; inspect its Helm/pod events" >&2
  bootstrap_status=1
fi

if ! wait "${cilium_pid}"; then
  echo "WARNING: Cilium bootstrap failed; OpenResty and Argo CD were not gated by it" >&2
  bootstrap_status=1
else
  if ! "${script_dir}/validate.sh"; then
    echo "WARNING: Cilium validation failed" >&2
    bootstrap_status=1
  fi
fi

if ! kubectl apply --filename "${repo_root}/config/argocd/applications/openresty.yaml"; then
  echo "WARNING: Argo CD Application creation failed" >&2
  bootstrap_status=1
fi

if ((bootstrap_status == 0)); then
  echo "k3d, Cilium, OpenResty, Argo CD, and the OpenResty Application are ready"
else
  echo "Platform bootstrap attempted all independent branches; inspect the warnings above" >&2
fi
exit "${bootstrap_status}"
