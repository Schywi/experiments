#!/usr/bin/env bash
#
# Tests for scripts/session-lab-experiment.sh itself. The script is the operator's
# entry point, so its contract is tested instead of trusted:
#
#   bash scripts/test-session-lab-experiment.sh
#
# No cluster is touched: everything here fails before the preflight.
set -uo pipefail

script="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/session-lab-experiment.sh"
failures=0

check() {
  local name="$1" expected="$2" output actual
  shift 2
  output="$("$@" 2>&1)"
  actual=$?
  if [[ "${actual}" -ne "${expected}" ]]; then
    printf 'FAIL %s: exit %d, want %d\n%s\n' "${name}" "${actual}" "${expected}" "${output}" >&2
    failures=$((failures + 1))
    return
  fi
  printf 'ok   %s\n' "${name}"
}

contains() {
  local name="$1" needle="$2"
  shift 2
  local output
  output="$("$@" 2>&1)"
  if ! grep -qF -- "${needle}" <<<"${output}"; then
    printf 'FAIL %s: output has no %q\n%s\n' "${name}" "${needle}" "${output}" >&2
    failures=$((failures + 1))
    return
  fi
  printf 'ok   %s\n' "${name}"
}

echo "== syntax"
check "bash -n accepts the script" 0 bash -n "${script}"

echo "== argument handling"
check "--help exits 0" 0 bash "${script}" --help
contains "--help documents both layers" "--layer session" bash "${script}" --help
check "no arguments is a usage error" 2 bash "${script}"
check "duration without a length is refused" 2 bash "${script}" --duration 600s --rate 0.1
contains "the refusal names the flags" "--mix" bash "${script}" --duration 600s --rate 0.1
check "nothing to run is refused" 2 bash "${script}" --policy reuse

echo "== --riot url list"
# Source the script: it only runs a experiment when executed, so its helpers can be
# tested without touching the cluster.
# shellcheck disable=SC1090
source "${script}"

urls="$(riot_urls 4)"
first="${urls%%,*}"
rest="${urls#*,}"
if [[ "${first}" != *"leagueoflegends.com/pt-br/champions/"* ]]; then
  printf 'FAIL first session is not the heavy champion page: %s\n' "${first}" >&2
  failures=$((failures + 1))
else
  printf 'ok   first session is the heavy champion page\n'
fi
splash_count="$(grep -o 'ddragon' <<<"${rest}" | wc -l)"
if [[ "${splash_count}" != "3" ]]; then
  printf 'FAIL expected 3 splash urls for 4 sessions, got %s\n' "${splash_count}" >&2
  failures=$((failures + 1))
else
  printf 'ok   the other sessions get splashes\n'
fi
distinct="$(tr ',' '\n' <<<"${urls}" | sort -u | wc -l)"
if [[ "${distinct}" != "4" ]]; then
  printf 'FAIL urls repeat: %s distinct of 4\n' "${distinct}" >&2
  failures=$((failures + 1))
else
  printf 'ok   every session gets a distinct page\n'
fi
if ! grep -q '_0\.jpg' <<<"${rest}"; then
  printf 'FAIL splashes carry no skin number\n' >&2
  failures=$((failures + 1))
else
  printf 'ok   splashes carry a skin number\n'
fi
# After a full pass over the champion list, the skin number steps.
last="$(riot_urls 20 | tr ',' '\n' | tail -1)"
if [[ "${last}" != *_1.jpg ]]; then
  printf 'FAIL session 20 is %s, want skin 1 once the champion list wraps\n' "${last}" >&2
  failures=$((failures + 1))
else
  printf 'ok   the skin number steps after a full champion pass\n'
fi
count="$(session_count --duration 60s --rate 0.5)"
if [[ "${count}" != "30" ]]; then
  printf 'FAIL session_count for 60s at 0.5/s = %s, want 30\n' "${count}" >&2
  failures=$((failures + 1))
else
  printf 'ok   session_count handles a fractional rate\n'
fi

echo "== arguments handed to the runner"
# The regression this guards: removing a flag with ${arr[@]/flag/} leaves an empty
# element, and Go's flag parser stops at it — the run then fails with "give --out"
# after the whole argument list was silently ignored.
mapfile -t kept < <(strip_flag --riot --duration 60s --riot --rate 0.5 --hold 30s)
if [[ " ${kept[*]} " == *" --riot "* ]]; then
  printf 'FAIL --riot survived the strip\n' >&2
  failures=$((failures + 1))
else
  printf 'ok   --riot is removed\n'
fi
empty=0
for arg in "${kept[@]}"; do
  [[ -z "${arg}" ]] && empty=$((empty + 1))
done
if (( empty > 0 )); then
  printf 'FAIL %d empty argument(s) left behind; the runner would stop parsing there\n' "${empty}" >&2
  failures=$((failures + 1))
else
  printf 'ok   no empty argument is passed through\n'
fi
if [[ "${kept[*]}" != "--duration 60s --rate 0.5 --hold 30s" ]]; then
  printf 'FAIL arguments changed: %s\n' "${kept[*]}" >&2
  failures=$((failures + 1))
else
  printf 'ok   the remaining arguments keep their order\n'
fi

echo "== summary"
if (( failures > 0 )); then
  printf '%d check(s) failed\n' "${failures}" >&2
  exit 1
fi
echo "all script checks passed"
