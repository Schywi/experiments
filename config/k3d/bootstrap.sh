#!/usr/bin/env bash

set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "${script_dir}/../.." && pwd)"

# Each stage is a hard gate. A fresh node never receives application workloads
# if an image import or platform component failed.
"${script_dir}/create.sh"
"${script_dir}/configure-node-dns.sh"
"${script_dir}/import-images.sh"
"${script_dir}/install-cilium.sh"
"${repo_root}/config/sealed-secrets/install.sh"
"${repo_root}/config/argocd/install.sh"
"${repo_root}/config/metallb/build-and-import.sh"
"${repo_root}/config/metallb/install.sh"
"${repo_root}/config/metallb/configure.sh"
"${script_dir}/enable-metallb-ingress.sh"
"${repo_root}/config/argocd/install-ingress.sh"
"${script_dir}/validate.sh"

echo "k3d, MetalLB, Cilium Ingress, and Argo CD are ready; Hubble UI and Argo CD are available through the assigned LoadBalancer IP"
