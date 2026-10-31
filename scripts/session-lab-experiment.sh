#!/usr/bin/env bash
#
# Run one session-lab experiment and print what it did.
#
# Ten-minute window, mixed lengths (the acceptance experiment):
#   scripts/session-lab-experiment.sh --duration 600s --rate 0.1 \
#       --mix 30s:40,2m:15,5m:5 --policy reuse
#
# Ten-minute window, one length:
#   scripts/session-lab-experiment.sh --duration 600s --rate 0.1 --hold 30s
#
# Fixed session count, mixed lengths:
#   scripts/session-lab-experiment.sh --sessions 20 --mix 30s:10,2m:10 --rate 0.3
#
# Destroy/rebuild instead of reuse/reset:
#   scripts/session-lab-experiment.sh --duration 600s --rate 0.1 --hold 60s --policy destroy
#
# Two layers, two questions. --layer infra puts one session in one pod, so the run
# measures claims, sandboxes and pod scheduling. --layer session packs up to
# --contexts-per-browser sessions into each Chromium, so the run measures the
# browser-session mechanic and the claim count stops tracking the session count.
#
#   scripts/session-lab-experiment.sh --layer infra   --duration 600s --rate 0.1 --mix ...
#   scripts/session-lab-experiment.sh --layer session --duration 600s --rate 0.8 --mix ...
#
# --layer session defaults to 8 contexts per browser and the ws CDP transport (a
# direct DevTools connection per browser); pass --contexts-per-browser N to change
# the packing. Without --layer, the runner's defaults apply: one context, http.
#
# --riot loads real pages instead of about:blank: one heavy champion page plus a
# distinct splash per session. Testing only, and it needs --layer session (targets
# only exist where there are browser contexts).
#
#   scripts/session-lab-experiment.sh --layer session --duration 60s --rate 0.5 --hold 30s --riot
#
# Refuses to start if the control plane is unhealthy, because a run that begins
# during an outage records a control-plane failure as if it were a capacity
# result. Prints the run's report path and then checks for leaked claims.
set -euo pipefail

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
app="${repo}/apps/session-lab"
out="${SESSION_LAB_OUT:-${repo}/docs/session-lab-runs}"
kubeconfig="${KUBECONFIG:-${HOME}/.kube/config}"
namespace="${SESSION_LAB_NAMESPACE:-sandbox-demo}"
pool="${SESSION_LAB_POOL:-browser-pool}"

# --riot: testing only. The first session loads a real champion page (it renders a
# full SPA, so it is the loud neighbour), and every other session loads its own
# splash image so a screenshot shows a different champion per session. The runner
# knows none of this; it only takes a list of URLs and cycles them.
riot_champions=(Yasuo LeBlanc Ahri Darius Jinx Lux Ezreal Katarina Garen Thresh
                LeeSin Vayne Zed Akali Riven Sona Teemo Ashe)
riot_heavy="https://www.leagueoflegends.com/pt-br/champions/leblanc/"
riot_splash_prefix="https://ddragon.leagueoflegends.com/cdn/img/champion/splash"

# riot_urls <count> prints a comma-separated list: one heavy page, then a splash
# per session with the champion rotating and the skin number stepping after each
# full pass, so two sessions never show the same image.
riot_urls() {
  local count="$1" urls=() index champion skin
  urls+=("${riot_heavy}")
  for ((index = 1; index < count; index++)); do
    champion="${riot_champions[$(((index - 1) % ${#riot_champions[@]}))]}"
    skin=$(((index - 1) / ${#riot_champions[@]}))
    urls+=("${riot_splash_prefix}/${champion}_${skin}.jpg")
  done
  (IFS=,; printf '%s' "${urls[*]}")
}

# session_count works out how many sessions a run will offer, from the same two
# spellings the runner accepts.
session_count() {
  local sessions="" duration="" rate=""
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --sessions|--sessions=*)
        [[ "$1" == --sessions=* ]] && sessions="${1#*=}" || { sessions="$2"; shift; } ;;
      --duration|--duration=*)
        [[ "$1" == --duration=* ]] && duration="${1#*=}" || { duration="$2"; shift; } ;;
      --rate|--rate=*)
        [[ "$1" == --rate=* ]] && rate="${1#*=}" || { rate="$2"; shift; } ;;
    esac
    shift
  done
  if [[ -n "${sessions}" ]]; then printf '%s' "${sessions}"; return; fi
  local seconds="${duration%s}"
  case "${duration}" in
    *m) seconds=$(( ${duration%m} * 60 )) ;;
    *h) seconds=$(( ${duration%h} * 3600 )) ;;
  esac
  awk -v seconds="${seconds}" -v rate="${rate}" 'BEGIN { printf "%d", seconds * rate }' 
}

# strip_flag <flag> <args...> prints the arguments without <flag>.
# Bash's ${arr[@]/x/} replaces the element with an empty string instead of removing
# it, and one empty argument stops Go's flag parser: everything after it is ignored,
# including --out. That is why the runner once refused to start with "give --out".
strip_flag() {
  local flag="$1"
  shift
  local kept=() arg
  for arg in "$@"; do
    [[ "${arg}" == "${flag}" ]] && continue
    kept+=("${arg}")
  done
  (( ${#kept[@]} )) && printf '%s\n' "${kept[@]}"
}

usage() {
  sed -n '2,20p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
  exit "${1:-0}"
}

# Check the arguments here, so a bad combination fails in a second instead of
# after a build and a cluster preflight.
validate_args() {
  local has_duration=0 has_rate=0 has_mix=0 has_hold=0 has_sessions=0 arg
  for arg in "$@"; do
    case "${arg}" in
      --duration|--duration=*) has_duration=1 ;;
      --rate|--rate=*) has_rate=1 ;;
      --mix|--mix=*) has_mix=1 ;;
      --hold|--hold=*) has_hold=1 ;;
      --sessions|--sessions=*) has_sessions=1 ;;
      --layer|--layer=*) ;;
      --layer)
        echo "--layer needs a value: infra or session" >&2; usage 2 ;;
    esac
  done
  if (( has_duration )); then
    (( has_rate )) || { echo "--duration needs --rate (sessions = rate x duration)" >&2; usage 2; }
    (( has_mix || has_hold )) || { echo "--duration needs --mix or --hold to set the session length" >&2; usage 2; }
  else
    (( has_sessions || has_mix )) || { echo "give --duration, or --sessions/--mix" >&2; usage 2; }
  fi
}



cleanup() {
      local exit_code=$?
      printf '%s script: teardown (exit %d)\n' "$(date -u +%H:%M:%S)" "${exit_code}"

      # Delete all claims created by session-lab. The label selector must stay on
      # one line: split, kubectl sees a bare -l and refuses the whole command.
      kubectl -n "${namespace}" delete sandboxclaims \
        -l app.kubernetes.io/managed-by=session-lab --wait=false 2>/dev/null || true

      # Delete any cold-started sandboxes.
      kubectl -n "${namespace}" delete sandboxes \
        -l app.kubernetes.io/managed-by=session-lab --wait=false 2>/dev/null || true

      # Optional: restore warm pool to a safe idle baseline (e.g. 2 replicas)
      # kubectl -n "${namespace}" scale sandboxwarmpool "${pool}" --replicas=2 2>/dev/null ||
      true

      exit "${exit_code}"
    }


# Every line the script prints is timestamped and mirrored into the run's
# directory, so a failed experiment leaves a log next to its report.
# The whole flow lives in main() so this file can be sourced by the script's own
# tests without launching a run.
main() {
  trap cleanup EXIT

  [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]] && usage 0
  [[ $# -ge 1 ]] || usage 2
  validate_args "$@"

  log_file="${SESSION_LAB_LOG:-${out}/session-lab-script-$(date -u +%Y%m%dT%H%M%SZ).log}"
  mkdir -p "${out}"
  exec > >(tee -a "${log_file}") 2>&1

  say()  { printf '%s script: %s\n' "$(date -u +%H:%M:%S)" "$*"; }
  warn() { printf '%s script: WARN %s\n' "$(date -u +%H:%M:%S)" "$*" >&2; }
  fail() { printf '%s script: ERROR %s\n' "$(date -u +%H:%M:%S)" "$*" >&2; }

  say "log file: ${log_file}"
  say "args: $*"

  echo "==> control plane"
  kubectl get --raw=/readyz >/dev/null || { echo "apiserver not ready; refusing to run" >&2; exit 1; }
  kubectl get node --no-headers | grep -q ' Ready ' || { echo "no Ready node; refusing to run" >&2; exit 1; }
  say "control plane ok, node Ready, pool replicas=$(kubectl -n "${namespace}" get sandboxwarmpool "${pool}" -o jsonpath='{.spec.replicas}' 2>/dev/null || echo '?')"

  echo "==> build"
  ( cd "${app}" && go build -o "${out}/session-lab" ./cmd/session-lab )
  mkdir -p "${out}"

  # --layer is this script's word, not the runner's: it decides how many sessions
  # one Chromium carries.
  layer_args=()
  for ((i = 1; i <= $#; i++)); do
    if [[ "${!i}" == "--layer" ]]; then
      j=$((i + 1))
      case "${!j}" in
        infra)   layer_args+=(--contexts-per-browser 1) ;;
        session) layer_args+=(--contexts-per-browser "${SESSION_LAB_CONTEXTS:-8}" --cdp-transport ws) ;;
        *) echo "--layer must be infra or session, got '${!j}'" >&2; exit 2 ;;
      esac
    fi
  done
  filtered=()
  skip=0
  for arg in "$@"; do
    if ((skip)); then skip=0; continue; fi
    if [[ "${arg}" == "--layer" ]]; then skip=1; continue; fi
    filtered+=("${arg}")
  done

  riot=0
  for arg in "${filtered[@]}"; do
    [[ "${arg}" == "--riot" ]] && riot=1
  done
  if (( riot )); then
    filtered=("${filtered[@]/--riot/}")
    count="$(session_count "${filtered[@]}")"
    if [[ -z "${count}" || "${count}" -lt 1 ]]; then
      echo "--riot needs a session count: pass --sessions, or --duration with --rate" >&2
      exit 2
    fi
    urls="$(riot_urls "${count}")"
    say "riot: 1 heavy page (${riot_heavy}) and $((count - 1)) splashes, one champion each"
    layer_args+=(--target-url "${urls}")
  fi

  if [[ -n "${SESSION_LAB_LOG_LEVEL:-}" ]]; then
    layer_args+=(--log-level "${SESSION_LAB_LOG_LEVEL}")
  fi

  if ((${#layer_args[@]})); then
    echo "==> layer: ${layer_args[*]}"
  else
    echo "==> layer: runner default (one session per pod)"
  fi

  echo "==> run"
  set -x
  "${out}/session-lab" "${filtered[@]}" "${layer_args[@]}" --namespace "${namespace}" --pool "${pool}" \
      --kubeconfig "${kubeconfig}" --out "${out}"
  set +x

  say "run finished; checking for leaked resources"
  left=$(kubectl -n "${namespace}" get sandboxclaims -o name 2>/dev/null | grep -c 'lab-' || true)
  if [[ "${left}" != "0" ]]; then
    warn "leaked lab- claims: ${left} (delete: kubectl -n ${namespace} delete sandboxclaims -l app.kubernetes.io/managed-by=session-lab)"
  else
    say "leaked lab- claims: 0"
  fi
  say "pool replicas now: $(kubectl -n "${namespace}" get sandboxwarmpool "${pool}" -o jsonpath='{.spec.replicas}')"
  say "paused annotation: \"$(kubectl -n "${namespace}" get sandboxwarmpool "${pool}" -o jsonpath='{.metadata.annotations.session-lab\.io/autoscaler}')\""
  say "newest report: $(ls -t "${out}"/*.json | head -1)"
  [[ "${left}" == "0" ]] || { echo "    WARNING: leaked claims; delete with: kubectl -n ${namespace} delete sandboxclaims -l app.kubernetes.io/managed-by=session-lab" >&2; exit 3; }
}

if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  main "$@"
fi
