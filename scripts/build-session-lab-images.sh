#!/usr/bin/env bash
#
# Build the session-lab image in-cluster with Kaniko, once per arm, and tag it.
#
#   scripts/build-session-lab-images.sh --tag v2
#   scripts/build-session-lab-images.sh --tag v2 --branch persist-probe
#   scripts/build-session-lab-images.sh --tag v2 --branch persist-probe --control-ref main --control-tag v1
#
# Kaniko clones the repository *inside the cluster*, so it builds what the **remote**
# has. A commit that was never pushed is not in the image, and a branch that does not
# exist on the remote cannot be built at all: the first attempt failed with
# `error resolving source context: reference not found` after the full timeout, which
# is kaniko saying it cloned the repository and then could not find the ref. Both
# conditions are checked here, before a Job is submitted, so that failure costs a
# second instead of a run.
#
# The tag is the arm: v1 is the sizing this repository shipped before the capacity
# work, v2 carries the published-demand source, the memory terms and the provisioning
# bound. A tag is never reused, because a run records its tag and a reused tag makes
# two different behaviours look identical afterwards.
#
# This script is kept in the repository on purpose: the build that produced a
# measurement has to be reproducible from the tree.
set -euo pipefail

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${repo}"

app="${APP:-session-lab}"
dest="${IMAGE_NAME:-session-lab-autoscaler}"
registry="${REGISTRY_ADDR:-192.168.0.28:5000}"
kaniko_ns="${KANIKO_NS:-registry}"
kaniko_timeout="${KANIKO_TIMEOUT:-900}"
branch=""
tag=""
control_ref=""
control_tag="v1"
allow_dirty=0
allow_unpushed=0

say() { printf '%s\n' "$*" >&2; }
die() { printf '%s\n' "$*" >&2; exit 1; }

usage() {
  awk 'NR > 2 && /^#/ { sub(/^# ?/, ""); print } NR > 2 && !/^#/ { exit }' "${BASH_SOURCE[0]}"
  exit "${1:-0}"
}

while (($# > 0)); do
  case "$1" in
    -h|--help) usage 0 ;;
    --branch) branch="${2:?--branch needs a value}"; shift 2 ;;
    --tag) tag="${2:?--tag needs a value}"; shift 2 ;;
    --control-ref) control_ref="${2:?--control-ref needs a value}"; shift 2 ;;
    --control-tag) control_tag="${2:?--control-tag needs a value}"; shift 2 ;;
    --allow-dirty) allow_dirty=1; shift ;;
    --allow-unpushed) allow_unpushed=1; shift ;;
    *) die "unknown argument $1 (see --help)" ;;
  esac
done

# The branch defaults to the one checked out; the tag defaults to the branch name.
[[ -n "${branch}" ]] || branch="$(git rev-parse --abbrev-ref HEAD)"
[[ -n "${tag}" ]] || tag="${branch}"
[[ "${branch}" != "HEAD" ]] || die "the checkout is detached; pass --branch"

[[ -d "apps/${app}" ]] || die "apps/${app} does not exist"
[[ -f "apps/${app}/Containerfile" ]] || die "apps/${app}/Containerfile is missing"

# --- the registry answers ---------------------------------------------------------
if command -v curl >/dev/null 2>&1; then
  curl -fsS --max-time 5 "http://${registry}/v2/" >/dev/null ||
    die "registry ${registry}/v2/ did not answer; is config/registry applied?"
fi

# --- the remote has this branch ---------------------------------------------------
origin_url="$(git remote get-url origin 2>/dev/null)" ||
  die "no origin remote; kaniko clones the remote, so there is nothing to build from"
host_path="${origin_url#*://}"

remote_heads="$(git ls-remote --heads origin 2>/dev/null | sed 's|.*refs/heads/||' | sort)" ||
  die "git ls-remote origin failed; a branch this host cannot see cannot be cloned in-cluster"
if ! grep -qx "${branch}" <<<"${remote_heads}"; then
  {
    printf 'branch %q is not on the remote.\n\n' "${branch}"
    printf 'Kaniko clones the remote, so a local-only branch cannot be built: this is the\n'
    printf '"error resolving source context: reference not found" failure.\n\n'
    printf 'push it first:\n    git push -u origin %s\n\n' "${branch}"
    printf 'branches the remote has:\n%s\n' "$(sed 's/^/    /' <<<"${remote_heads}")"
  } >&2
  exit 1
fi

# --- the remote has this work -----------------------------------------------------
unpushed="$(git log --oneline "origin/${branch}..HEAD" -- "apps/${app}" 2>/dev/null || true)"
if [[ -n "${unpushed}" ]]; then
  {
    printf 'commits under apps/%s are not on origin/%s, so the image will not contain them:\n%s\n\n' \
      "${app}" "${branch}" "$(sed 's/^/    /' <<<"${unpushed}")"
  } >&2
  if ((allow_unpushed == 0)); then
    printf 'push them, or pass --allow-unpushed to build the older tree on purpose.\n' >&2
    exit 1
  fi
fi

dirty="$(git status --porcelain -- "apps/${app}" 2>/dev/null || true)"
if [[ -n "${dirty}" ]]; then
  {
    printf 'uncommitted changes under apps/%s, which the in-cluster build cannot see:\n%s\n\n' \
      "${app}" "$(sed 's/^/    /' <<<"${dirty}")"
  } >&2
  if ((allow_dirty == 0)); then
    printf 'commit them, or pass --allow-dirty to build the committed tree anyway.\n' >&2
    exit 1
  fi
fi

say "==> building ${dest}:${tag}"
say "    branch  ${branch} @ $(git rev-parse --short "origin/${branch}" 2>/dev/null || echo '?')"
say "    context git://${host_path}#refs/heads/${branch}, subpath apps/${app}"

# --- the job ----------------------------------------------------------------------
build() {  # build <ref> <image-tag>
  local ref="$1" image_tag="$2" job waited=0 failed succeeded
  # One Job name per ref, so a control build does not overwrite the arm under test.
  local name="kaniko-${app}-$(tr '/' '-' <<<"${ref}")"
  job="$(mktemp --suffix=.yaml)"
  cat >"${job}" <<YAML
apiVersion: batch/v1
kind: Job
metadata:
  name: ${name}
  namespace: ${kaniko_ns}
  labels:
    app.kubernetes.io/name: kaniko
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
          image: ${KANIKO_IMAGE:-gcr.io/kaniko-project/executor:v1.23.2}
          args:
            - "--context=git://${host_path}#refs/heads/${ref}"
            - "--context-sub-path=apps/${app}"
            - "--dockerfile=Containerfile"
            - "--destination=${registry}/${dest}:${image_tag}"
            - "--insecure"
            - "--verbosity=info"
YAML
  kubectl -n "${kaniko_ns}" delete job "${name}" --ignore-not-found >/dev/null
  kubectl apply -f "${job}" >/dev/null
  rm -f -- "${job}"
  say "==> ${name} -> ${registry}/${dest}:${image_tag} (up to ${kaniko_timeout}s)"

  # Wait on the condition, not on the clock. A rejected build is decided in seconds;
  # the full-timeout wait only made it look like a timeout, and the reason is the
  # first line of the log, not the last.
  while ((waited < kaniko_timeout)); do
    failed="$(kubectl -n "${kaniko_ns}" get job "${name}" -o jsonpath='{.status.conditions[?(@.type=="Failed")].status}' 2>/dev/null || true)"
    succeeded="$(kubectl -n "${kaniko_ns}" get job "${name}" -o jsonpath='{.status.conditions[?(@.type=="Complete")].status}' 2>/dev/null || true)"
    if [[ "${succeeded}" == "True" ]]; then
      say "==> pushed ${registry}/${dest}:${image_tag}"
      return 0
    fi
    if [[ "${failed}" == "True" ]]; then
      say "==> build failed after ${waited}s; the reason is at the top of the log:"
      kubectl -n "${kaniko_ns}" logs "job/${name}" 2>/dev/null | head -20 >&2 || true
      say "==> last lines:"
      kubectl -n "${kaniko_ns}" logs "job/${name}" 2>/dev/null | tail -10 >&2 || true
      return 1
    fi
    sleep 5
    waited=$((waited + 5))
  done
  say "==> timed out after ${kaniko_timeout}s; last lines:"
  kubectl -n "${kaniko_ns}" logs "job/${name}" 2>/dev/null | tail -20 >&2 || true
  return 1
}

build "${branch}" "${tag}"

if [[ -n "${control_ref}" ]]; then
  if ! grep -qx "${control_ref}" <<<"${remote_heads}"; then
    die "control ref ${control_ref} is not a branch on the remote either"
  fi
  say
  build "${control_ref}" "${control_tag}"
else
  say
  say "control arm not built. Cut it with the same script:"
  say "  $0 --branch ${branch} --tag ${tag} --control-ref <branch-without-the-v2-work> --control-tag ${control_tag}"
fi

say
say "point the charts at the arm:"
say "  config/session-lab/values.yaml   image.tag: ${tag}"
say "  config/queue-lab/values.yaml     autoscaler.image.tag: ${tag}"
say "then: scripts/queue-v2-run.sh --tag=${tag} --mix 30s:1500,2m:900,5m:600 --rate 35"
