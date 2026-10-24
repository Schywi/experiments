#!/usr/bin/env bash
#
# Create the git credential Secret a Kargo Stage uses to push promotions.
#
#   GITHUB_TOKEN=... config/kargo/git-credentials.sh
#   GITHUB_TOKEN="$(gh auth token)" config/kargo/git-credentials.sh
#
# The Secret lands in the Project namespace and is labelled
# `kargo.akuity.io/cred-type: git`, which is how Kargo matches it to the
# repository URL used by the `git-clone` / `git-push` steps. It is created with
# kubectl and never committed: nothing in this directory contains a token.
#
# GitHub tokens are used as the password with any non-empty username.
set -euo pipefail

namespace="${KARGO_PROJECT_NAMESPACE:-pilot}"
secret_name="${KARGO_GIT_SECRET_NAME:-github-credentials}"
token="${GITHUB_TOKEN:-}"
repo_url="${KARGO_REPO_URL:-https://github.com/Schywi/experiments.git}"

[[ -n "${token}" ]] || { echo "GITHUB_TOKEN is required" >&2; exit 2; }

export KUBECONFIG="${KUBECONFIG:-/etc/rancher/k3s/k3s.yaml}"

kubectl create namespace "${namespace}" --dry-run=client -o yaml | kubectl apply -f - >/dev/null

kubectl -n "${namespace}" create secret generic "${secret_name}" \
  --from-literal=repoURL="${repo_url}" \
  --from-literal=username=kargo \
  --from-literal=password="${token}" \
  --dry-run=client -o yaml |
  kubectl apply -f - >/dev/null

kubectl -n "${namespace}" label secret "${secret_name}" \
  kargo.akuity.io/cred-type=git --overwrite >/dev/null

echo "secret/${secret_name} in ${namespace} labelled kargo.akuity.io/cred-type=git"
