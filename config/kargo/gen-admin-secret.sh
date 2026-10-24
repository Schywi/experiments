#!/usr/bin/env bash
#
# Admin credentials for Kargo, created out of band.
#
# Kargo has no anonymous mode: with `adminAccount.enabled: false` every API
# request is refused and the web UI is unusable. The way to keep a login-free
# UI is to enable the admin account and never type the password: the kargo
# gateway injects a token it obtains from this account.
#
# Two Secrets, in namespace kargo:
#
#   kargo-admin-credentials  ADMIN_ACCOUNT_PASSWORD_HASH, ADMIN_ACCOUNT_TOKEN_SIGNING_KEY
#                            referenced by `api.secret.name` in values.yaml. Not
#                            named `kargo-api`: that is a chart-managed Secret
#                            which Helm would delete on upgrade.
#   kargo-gateway-admin   KARGO_ADMIN_PASSWORD (plaintext, for the proxy login)
#
# Idempotent: an existing, populated `kargo-admin-credentials` Secret is reused,
# so a reinstall keeps the same password and signing key.
#
#   config/kargo/gen-admin-secret.sh
#   KARGO_ADMIN_PASSWORD=... config/kargo/gen-admin-secret.sh   # choose your own
set -euo pipefail

namespace="${KARGO_NAMESPACE:-kargo}"
export KUBECONFIG="${KUBECONFIG:-/etc/rancher/k3s/k3s.yaml}"

kubectl create namespace "${namespace}" --dry-run=client -o yaml | kubectl apply -f - >/dev/null

have_hash="$(kubectl -n "${namespace}" get secret kargo-admin-credentials \
  -o jsonpath='{.data.ADMIN_ACCOUNT_PASSWORD_HASH}' 2>/dev/null || true)"

if [[ -n "${have_hash}" ]]; then
  echo "==> secret/kargo-admin-credentials already populated; reusing admin credentials"
  exit 0
fi

# bcrypt via htpasswd (Apache bcrypt, $2y$) — Kargo accepts bcrypt hashes as-is.
command -v htpasswd >/dev/null 2>&1 || {
  echo "htpasswd (apache2-utils/httpd-tools) is required to hash the password" >&2
  exit 1
}

password="${KARGO_ADMIN_PASSWORD:-$(openssl rand -base64 32 | tr -d '=+/' | cut -c1-24)}"
hash="$(htpasswd -bnBC 10 "" "${password}" | tr -d ':\n')"
signing_key="$(openssl rand -base64 29 | tr -d '=+/' | cut -c1-32)"

kubectl -n "${namespace}" create secret generic kargo-admin-credentials \
  --from-literal=ADMIN_ACCOUNT_PASSWORD_HASH="${hash}" \
  --from-literal=ADMIN_ACCOUNT_TOKEN_SIGNING_KEY="${signing_key}" \
  --dry-run=client -o yaml | kubectl apply -f - >/dev/null

kubectl -n "${namespace}" create secret generic kargo-gateway-admin \
  --from-literal=KARGO_ADMIN_PASSWORD="${password}" \
  --dry-run=client -o yaml | kubectl apply -f - >/dev/null

echo "==> created secret/kargo-admin-credentials and secret/kargo-gateway-admin in ${namespace}"
echo "    admin password: ${password}"
echo "    (only the gateway needs it; the UI logs itself in)"
