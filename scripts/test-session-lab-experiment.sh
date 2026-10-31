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

echo "== summary"
if (( failures > 0 )); then
  printf '%d check(s) failed\n' "${failures}" >&2
  exit 1
fi
echo "all script checks passed"
