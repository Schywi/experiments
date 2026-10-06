#!/usr/bin/env bash
#
# Sequential native-k3s platform bootstrap. Each stage is a hard gate: a fresh
# node never receives workloads if the CNI or Argo CD failed. Requires root
# (install-k3s.sh installs a system service). Does not touch the k3d profile.

set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "${script_dir}/../.." && pwd)"

# install-k3s.sh writes this kubeconfig; later stages read it.
export KUBECONFIG="${KUBECONFIG:-/etc/rancher/k3s/k3s.yaml}"

"${script_dir}/install-k3s.sh"
"${script_dir}/install-cilium.sh"
"${repo_root}/config/argocd/install.sh"
"${repo_root}/config/argocd/install-ingress.sh"
"${script_dir}/validate.sh"

echo "Native k3s, Cilium, Hubble, and Argo CD are ready"
