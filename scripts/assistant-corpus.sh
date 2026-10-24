#!/usr/bin/env bash
# Build/refresh the assistant documentation corpus ConfigMap from tracked repo
# docs (M6). Paths are flattened ('/' -> '__') because ConfigMap keys cannot
# contain slashes. Run from the repo root with KUBECONFIG set.
#
#   config/registry/install.sh                # if not already running
#   scripts/assistant-corpus.sh
#   kubectl -n assistant rollout restart deploy/assistant   # pick up the corpus
set -euo pipefail

ns="${ASSISTANT_NS:-assistant}"
name="${ASSISTANT_CORPUS_NAME:-assistant-corpus}"
tmp="$(mktemp -d)"
trap 'rm -rf -- "${tmp}"' EXIT

shopt -s nullglob
files=(
  README.md
  config/*/README.md
  config/README.md
  apps/*/README.md
  research/*.md
)
for f in "${files[@]}"; do
  [ -f "${f}" ] || continue
  cp -f "${f}" "${tmp}/${f//\//__}"
done

if [ -z "$(ls -A "${tmp}")" ]; then
  echo "no corpus files found" >&2
  exit 1
fi

kubectl -n "${ns}" create configmap "${name}" \
  --from-file="${tmp}" --dry-run=client -o yaml | kubectl apply -f -

echo "applied configmap/${name} in namespace ${ns}:"
ls -1 "${tmp}"
