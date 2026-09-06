#!/usr/bin/env bash

set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "${script_dir}/../.." && pwd)"

"${script_dir}/create.sh"
"${script_dir}/configure-node-dns.sh"

bootstrap_status=0
if ! "${script_dir}/import-images.sh"; then
  echo "WARNING: local image import failed; continuing independent platform branches" >&2
  bootstrap_status=1
fi

# Cilium and Argo CD are sibling branches after k3d creation. Keep the Cilium
# Helm wait from blocking Argo CD when the node cannot yet pull or run images.
"${script_dir}/install-cilium.sh" &
cilium_pid=$!

"${repo_root}/config/argocd/install.sh" &
argocd_pid=$!

if ! wait "${argocd_pid}"; then
  echo "WARNING: Argo CD bootstrap failed; inspect its Helm/pod events" >&2
  bootstrap_status=1
fi

if ! wait "${cilium_pid}"; then
  echo "WARNING: Cilium bootstrap failed; Argo CD was not gated by it" >&2
  bootstrap_status=1
else
  if ! "${script_dir}/validate.sh"; then
    echo "WARNING: Cilium validation failed" >&2
    bootstrap_status=1
  fi
fi

if ((bootstrap_status == 0)); then
  echo "k3d, Cilium Ingress, and Argo CD are ready; Hubble UI is at http://localhost:8080/"
else
  echo "Platform bootstrap attempted all independent branches; inspect the warnings above" >&2
fi
exit "${bootstrap_status}"
