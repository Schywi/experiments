#!/usr/bin/env bash
#
# TLS material for Kargo, generated on the host so cert-manager is not needed.
#
# Kargo's chart wants a cert for the API server and one for its internal
# Kubernetes webhooks server (the API server will not call a plain-HTTP
# webhook). With `selfSignedCert: true` the chart creates two cert-manager
# `Certificate` resources; cert-manager is three pods this node does not have
# memory for. Instead this script signs both certs with a local CA and loads
# them as plain TLS Secrets:
#
#   kargo-api-cert              -> api.tls.secretName
#   kargo-webhooks-server-cert  -> webhooksServer.tls.secretName
#
# and prints the CA PEM path, which install.sh passes as
# `webhooksServer.tls.caBundle` so the Kubernetes API server trusts the webhook
# cert. Nothing is generated into the repository; the default output directory
# lives under $XDG_DATA_HOME.
#
# Idempotent: existing CA and certs are reused, so a re-run is a no-op. Delete
# the directory to rotate.
set -euo pipefail

namespace="${KARGO_NAMESPACE:-kargo}"
cert_dir="${KARGO_CERT_DIR:-${XDG_DATA_HOME:-${HOME}/.local/share}/kargo/certs}"
api_ip="${KARGO_API_IP:-192.168.0.248}"
days="${KARGO_CERT_DAYS:-3650}"

mkdir -p "${cert_dir}"
cd "${cert_dir}"

if [[ ! -f ca.key || ! -f ca.crt ]]; then
  echo "==> generating CA"
  openssl req -x509 -newkey rsa:2048 -nodes -days "${days}" \
    -keyout ca.key -out ca.crt \
    -subj "/CN=kargo-local-ca" >/dev/null 2>&1
fi

issue() {
  local name="$1" san="$2"
  if [[ -f "${name}.crt" && -f "${name}.key" ]]; then
    echo "==> ${name}: reusing existing cert"
    return
  fi
  echo "==> ${name}: signing"
  openssl req -newkey rsa:2048 -nodes -keyout "${name}.key" -out "${name}.csr" \
    -subj "/CN=${name}" >/dev/null 2>&1
  cat >"${name}.ext" <<EXT
subjectAltName=${san}
keyUsage=digitalSignature,keyEncipherment
extendedKeyUsage=serverAuth
EXT
  openssl x509 -req -in "${name}.csr" -CA ca.crt -CAkey ca.key \
    -CAcreateserial -days "${days}" -sha256 \
    -extfile "${name}.ext" -out "${name}.crt" >/dev/null 2>&1
  rm -f "${name}.csr" "${name}.ext"
}

issue kargo-api \
  "DNS:kargo-api,DNS:kargo-api.${namespace},DNS:kargo-api.${namespace}.svc,DNS:kargo-api.${namespace}.svc.cluster.local,DNS:localhost,IP:127.0.0.1,IP:${api_ip}"
issue kargo-webhooks-server \
  "DNS:kargo-webhooks-server,DNS:kargo-webhooks-server.${namespace},DNS:kargo-webhooks-server.${namespace}.svc,DNS:kargo-webhooks-server.${namespace}.svc.cluster.local"

if command -v kubectl >/dev/null 2>&1; then
  kubectl create namespace "${namespace}" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  for pair in "kargo-api-cert:kargo-api" "kargo-webhooks-server-cert:kargo-webhooks-server"; do
    secret="${pair%%:*}"
    name="${pair##*:}"
    kubectl -n "${namespace}" create secret tls "${secret}" \
      --cert="${name}.crt" --key="${name}.key" \
      --dry-run=client -o yaml | kubectl apply -f - >/dev/null
    echo "==> secret/${secret} in namespace ${namespace}"
  done
else
  echo "kubectl not found; secrets not created" >&2
fi

echo "${cert_dir}/ca.crt"
