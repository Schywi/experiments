#!/usr/bin/env bash

set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "${script_dir}/../.." && pwd)"

"${script_dir}/create.sh"
"${script_dir}/import-images.sh"
"${script_dir}/install-cilium.sh"
"${script_dir}/validate.sh"

helm upgrade --install openresty "${repo_root}/config/openresty" \
  --namespace openresty \
  --create-namespace \
  --wait \
  --timeout "${HELM_TIMEOUT:-10m}"

"${repo_root}/config/argocd/install.sh"

kubectl apply \
  --filename "${repo_root}/config/argocd/applications/openresty.yaml"

echo "k3d, Cilium, OpenResty, Argo CD, and the OpenResty Application are ready"
