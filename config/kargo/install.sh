#!/usr/bin/env bash
#
# Install or upgrade Kargo with the values in this directory.
#
#   config/kargo/install.sh
#   KARGO_CHART_VERSION=1.12.0 config/kargo/install.sh
#
# No cert-manager: gen-certs.sh produces the two TLS Secrets and a local CA, and
# the CA is injected as `webhooksServer.tls.caBundle` so the Kubernetes API
# server trusts Kargo's webhook endpoint.
#
# Login: Kargo has no anonymous mode, so gen-admin-secret.sh creates an admin
# account with a generated password and kargo-gateway logs in with it and
# injects the token. The UI is then at http://192.168.0.248/ with no form:
#
#   curl -s http://192.168.0.248/v1beta1/projects     # JSON, no credentials
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
chart_version="${KARGO_CHART_VERSION:-1.12.0}"
namespace="${KARGO_NAMESPACE:-kargo}"

export KUBECONFIG="${KUBECONFIG:-/etc/rancher/k3s/k3s.yaml}"

ca_crt="$("${script_dir}/gen-certs.sh" | tail -1)"
[[ -s "${ca_crt}" ]] || { echo "gen-certs.sh did not produce a CA" >&2; exit 1; }

"${script_dir}/gen-admin-secret.sh"

echo "==> helm upgrade --install kargo ${chart_version} in namespace ${namespace}"
helm upgrade --install kargo \
  oci://ghcr.io/akuity/kargo-charts/kargo \
  --version "${chart_version}" \
  --namespace "${namespace}" \
  --create-namespace \
  --values "${script_dir}/values.yaml" \
  --set-file "webhooksServer.tls.caBundle=${ca_crt}" \
  --wait --timeout 10m

echo "==> kargo-gateway (proxied login, LoadBalancer on the Cilium lan-pool)"
kubectl apply -f "${script_dir}/../argocd/applications/kargo-gateway.yaml"

kubectl -n "${namespace}" get deploy,pod
kubectl -n "${namespace}" get svc kargo-gateway
