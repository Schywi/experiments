#!/usr/bin/env bash
#
# The whole sequence in one command: build the arm, apply the chart with that tag,
# prove the autoscaler published it, then run the arm.
#
#   scripts/queue-v2-up.sh --tag persist-probe -- --mix 30s:1500,2m:900,5m:600 --rate 35
#   scripts/queue-v2-up.sh --tag persist-probe --apply -- --mix 30s:1500,2m:900,5m:600 --rate 35
#   scripts/queue-v2-up.sh --tag persist-probe --skip-build --apply --no-run
#
# It exists because the three steps were three scripts, and the middle one — applying
# the chart with the tag the image was actually built under — is the step that silently
# fails: this deployment sat in ImagePullBackOff asking for :v2 while the registry held
# :persist-probe, so the autoscaler never published a ConfigMap and the run could not
# say which arm it measured.
#
# --apply is required before anything is sent to the cluster. Applying a committed
# manifest is the operator's step; this script will not do it by accident.
set -euo pipefail

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${repo}"

tag=""
branch=""
apply=0
skip_build=0
skip_run=0
dry_run=0
auto_commit=1
commit_msg="chore(wip): snapshot before the in-cluster build"
build_flags=()
run_args=()

say() { printf '%s\n' "$*" >&2; }
die() { printf '%s\n' "$*" >&2; exit 1; }

usage() {
  awk 'NR > 2 && /^#/ { sub(/^# ?/, ""); print } NR > 2 && !/^#/ { exit }' "${BASH_SOURCE[0]}"
  exit "${1:-0}"
}

while (($# > 0)); do
  case "$1" in
    -h|--help) usage 0 ;;
    --tag) tag="${2:?--tag needs a value}"; shift 2 ;;
    --tag=*) tag="${1#*=}"; shift ;;
    --branch) branch="${2:?--branch needs a value}"; shift 2 ;;
    --branch=*) branch="${1#*=}"; shift ;;
    --apply) apply=1; shift ;;
    --allow-dirty) build_flags+=(--allow-dirty); shift ;;
    --allow-unpushed) build_flags+=(--allow-unpushed); shift ;;
    --no-commit) auto_commit=0; shift ;;
    --message) commit_msg="${2:?--message needs a value}"; shift 2 ;;
    --skip-build) skip_build=1; shift ;;
    --no-run) skip_run=1; shift ;;
    --dry-run) dry_run=1; shift ;;
    --) shift; run_args=("$@"); break ;;
    *) die "unknown argument $1. This script's flags go BEFORE the -- separator; only workload flags go after it." ;;
  esac
done

[[ -n "${branch}" ]] || branch="$(git rev-parse --abbrev-ref HEAD)"
[[ -n "${tag}" ]] || tag="${branch}"

run() {  # run <description> <command...>
  say "==> $1"
  if ((dry_run == 1)); then
    shift
    printf '    '; printf '%q ' "$@"; printf '\n'
    return 0
  fi
  shift
  "$@"
}

# --- 0: the remote has this tree ---------------------------------------------------
# Kaniko clones the remote, so anything uncommitted or unpushed is simply absent from
# the image. That is what put a :v2 Deployment in ImagePullBackOff over an image built
# as :persist-probe. This step commits and pushes the working tree so one command is
# enough; --no-commit keeps the tree untouched and lets the build script refuse.
if ((auto_commit == 1 && skip_build == 0)); then
  dirty="$(git status --porcelain)"
  unpushed="$(git log --oneline "origin/${branch}..HEAD" 2>/dev/null || true)"
  if [[ -n "${dirty}" || -n "${unpushed}" ]]; then
    say "==> commit and push ${branch} so the in-cluster build sees this tree"
    if ((dry_run == 1)); then
      printf '    git add -A && git commit -m %q && git push -u origin %s\n' "${commit_msg}" "${branch}"
    else
      git add -A
      git commit -m "${commit_msg}" || say "    nothing to commit"
      git push -u origin "${branch}"
    fi
  else
    say "==> ${branch} is committed and pushed"
  fi
fi

# --- 1: the image the cluster can pull --------------------------------------------
if ((skip_build == 0)); then
  run "build ${tag} from ${branch}" \
    bash "${repo}/scripts/build-session-lab-images.sh" --tag "${tag}" --branch "${branch}" "${build_flags[@]}"
else
  say "==> skipping the build; ${tag} must already be in the registry"
fi

# --- 2: the chart, pinned to that tag ---------------------------------------------
# The tag is passed at apply time rather than trusting values.yaml, because the two
# drifting apart is exactly how this deployment ended up in ImagePullBackOff asking
# for an image nobody built.
render_cmd="helm template queue-lab ${repo}/config/queue-lab --set autoscaler.image.tag=${tag} | kubectl apply -f -"
if ((dry_run == 1)); then
  say "==> render and apply queue-lab with autoscaler.image.tag=${tag}"
  printf '    %s\n' "${render_cmd}"
elif ((apply == 1)); then
  say "==> render and apply queue-lab with autoscaler.image.tag=${tag}"
  helm template queue-lab "${repo}/config/queue-lab" --set "autoscaler.image.tag=${tag}" | kubectl apply -f -
else
  say "==> not applying; the chart for tag ${tag} is sent with:"
  printf '    %s\n' "${render_cmd}"
fi

if ((apply == 1 && dry_run == 0)); then
  run "wait for the rollout" kubectl -n queue-lab rollout status deploy/queue-lab-autoscaler --timeout=120s

  # The point of the whole exercise: the tag the pod runs has to be the tag this run
  # claims. Read back what the autoscaler publishes about itself.
  deployed="$(kubectl -n queue-lab get configmap queue-lab-autoscaler \
    -o jsonpath='{.data.settings\.json}' 2>/dev/null | sed -n 's/.*"image_tag": *"\([^"]*\)".*/\1/p')"
  if [[ "${deployed}" != "${tag}" ]]; then
    die "the autoscaler reports image_tag=${deployed:-<none>}, expected ${tag}.
    Check: kubectl -n queue-lab get pods; kubectl -n queue-lab describe pod -l component=autoscaler"
  fi
  say "==> autoscaler is publishing image_tag=${tag}"
fi

# --- 3: the arm --------------------------------------------------------------------
if ((skip_run == 1)); then
  say "==> not running; start it with: scripts/queue-v2-run.sh --tag=${tag} -- ${run_args[*]}"
  exit 0
fi
run "run the arm" bash "${repo}/scripts/queue-v2-run.sh" "--tag=${tag}" -- "${run_args[@]}"
