#!/usr/bin/env bash
#
# Build the session-lab image in-cluster with Kaniko, once per arm, and tag it.
#
#   scripts/build-session-lab-images.sh v2                     # the arm under test
#   scripts/build-session-lab-images.sh v2 <ref-for-v1> v1     # also cut the control
#
# Kaniko clones the repository *inside* the cluster and pushes straight to the
# in-cluster registry, so nothing here needs docker, buildah or host root. The tag
# is the arm: v1 is the sizing this repository shipped before the capacity work,
# v2 carries the published-demand source, the memory terms and the provisioning
# bound. A tag is never reused, because a run records its tag and a reused tag
# makes two different behaviours look identical afterwards.
#
# This script is kept in the repository on purpose: the build that produced a
# measurement has to be reproducible from the tree, not from a shell history.
set -euo pipefail

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
branch="${1:-v2}"
v1_ref="${2:-}"
v1_tag="${3:-v1}"
registry="${REGISTRY_ADDR:-192.168.0.28:5000}"
dest="${IMAGE_NAME:-session-lab-autoscaler}"
kaniko="${repo}/config/registry/kaniko-build.sh"

say() { printf '%s\n' "$*" >&2; }
die() { printf '%s\n' "$*" >&2; exit 1; }

[[ -x "${kaniko}" ]] || die "${kaniko} is missing or not executable"
[[ "${branch}" != "v1" ]] || die "v1 is a tag, not a branch: pass the branch to build as \$1"

# --- the registry answers ---------------------------------------------------------
if command -v curl >/dev/null 2>&1; then
  curl -fsS --max-time 5 "http://${registry}/v2/" >/dev/null ||
    die "registry ${registry}/v2/ did not answer; is config/registry applied?"
fi

say "building ${dest}:${branch} from branch ${branch}"
"${kaniko}" session-lab "${branch}" "${branch}" "${dest}"

if [[ -n "${v1_ref}" ]]; then
  say "building ${dest}:${v1_tag} from ref ${v1_ref} (the control arm)"
  "${kaniko}" session-lab "${v1_ref}" "${v1_tag}" "${dest}"
else
  say "no v1 ref given: only ${dest}:${branch} was built"
  say "  cut the control with: $0 ${branch} <ref-before-this-change> ${v1_tag}"
fi

say
say "chosen arm:"
say "  config/session-lab/values.yaml      image.tag: ${branch}"
say "  config/queue-lab/values.yaml        autoscaler.image.tag: ${branch}"
say "Apply the chart, then run scripts/queue-v2-run.sh --tag=${branch}."
