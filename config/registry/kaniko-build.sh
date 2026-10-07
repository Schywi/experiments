#!/usr/bin/env bash
#
# In-cluster image build with Kaniko — no buildah, no docker, no host root.
#
# Kaniko runs as a Job. It clones the repository at a branch *inside the
# cluster*, builds apps/<app>, and pushes straight to the in-cluster registry.
#
#   config/registry/kaniko-build.sh assistant
#   config/registry/kaniko-build.sh assistant native-k3s-gpu-models local
#
# Registry address: the registry runs hostNetwork on the node (hostPort 5000),
# so a pod must reach it at the node IP, not 127.0.0.1 (a pod's loopback is the
# pod itself). Override with REGISTRY_ADDR.
#
# Note: the executor image is gcr.io/kaniko-project/executor — a deliberate
# exception to the repository's Docker Hub-only image policy, requested for
# in-cluster builds.
set -euo pipefail

app="${1:?usage: kaniko-build.sh <app> [branch] [tag] [dest-name]}"
branch="${2:-native-k3s-gpu-models}"
tag="${3:-local}"
dest_name="${4:-${app}}"
repo="${REPO_URL:-https://github.com/Schywi/experiments.git}"
registry="${REGISTRY_ADDR:-192.168.0.28:5000}"
ns="${KANIKO_NS:-registry}"
executor="${KANIKO_IMAGE:-gcr.io/kaniko-project/executor:v1.23.2}"
name="kaniko-${app}"

git_url="git://${repo#*://}#refs/heads/${branch}"

echo "==> building apps/${app} @ ${branch} -> ${registry}/${dest_name}:${tag}"
echo "    git context: ${git_url}"

job="$(mktemp --suffix=.yaml)"
trap 'rm -f -- "${job}"' EXIT
cat >"${job}" <<YAML
apiVersion: batch/v1
kind: Job
metadata:
  name: ${name}
  namespace: ${ns}
  labels:
    app.kubernetes.io/name: kaniko
    app.kubernetes.io/part-of: assistant-lab
spec:
  backoffLimit: 0
  ttlSecondsAfterFinished: 900
  template:
    metadata:
      labels:
        app.kubernetes.io/name: kaniko
    spec:
      restartPolicy: Never
      containers:
        - name: kaniko
          image: ${executor}
          args:
            - "--context=${git_url}"
            - "--context-sub-path=apps/${app}"
            - "--dockerfile=Containerfile"
            - "--destination=${registry}/${dest_name}:${tag}"
            - "--insecure"
            - "--verbosity=info"
YAML

kubectl -n "${ns}" delete job "${name}" --ignore-not-found >/dev/null
kubectl apply -f "${job}"
echo "==> waiting for ${name} (up to 10m)"
if ! kubectl -n "${ns}" wait --for=condition=complete "job/${name}" --timeout=600s; then
  echo "==> build failed; last logs:" >&2
  kubectl -n "${ns}" logs "job/${name}" --tail=60 >&2 || true
  exit 1
fi
echo "==> pushed ${registry}/${dest_name}:${tag}"
