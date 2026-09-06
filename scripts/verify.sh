#!/usr/bin/env bash
# Deterministic, local verification for the bounded Worm experiment.
#
# This script deliberately does not contact a Kubernetes cluster, Docker, a
# registry, or a Helm repository. It validates only checked-out source and
# locally renderable charts.
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "${script_dir}/.." && pwd)"

fail() {
  printf 'verify: %s\n' "$*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "required command is unavailable: $1"
}

section() {
  printf '\n==> %s\n' "$1"
}

run_go_checks() {
  section 'Go tests and vet'
  require_command go
  (
    cd "${repo_root}/apps/controller"
    go test ./...
    go vet ./...
  )
}

run_elixir_checks() {
  section 'Elixir tests'
  require_command mix
  (
    cd "${repo_root}/apps/regression"
    MIX_ENV=test mix test
  )
}

run_worker_checks() {
  section 'Worker static and component checks'
  (
    cd "${repo_root}/apps/worker"
    ./test/static-check.sh

    # The component artifact is intentionally produced by the selected
    # package-first runtime work. If it is present, reject an empty artifact;
    # if it is absent, the static Containerfile contract remains the current
    # deterministic check until that work package lands.
    if [[ -e dist/worm.component.wasm ]]; then
      [[ -s dist/worm.component.wasm ]] || fail 'worker component exists but is empty'
      printf 'worker component artifact: present\n'
    else
      printf 'worker component artifact: not present (static contract checked)\n'
    fi
  )
}

render_charts() {
  local output_dir="$1"
  local chart_file chart_dir chart_name rendered
  local -a chart_files=()

  require_command helm
  while IFS= read -r -d '' chart_file; do
    chart_files+=("${chart_file}")
  done < <(find "${repo_root}/config" "${repo_root}/apps" -type f -name Chart.yaml -print0 | sort -z)

  ((${#chart_files[@]} > 0)) || fail 'no Helm charts found under config/ or apps/'

  for chart_file in "${chart_files[@]}"; do
    chart_dir="$(dirname -- "${chart_file}")"
    chart_name="$(basename -- "${chart_dir}")"
    rendered="${output_dir}/${chart_name}.yaml"

    printf 'linting %s\n' "${chart_dir#"${repo_root}/"}"
    helm lint --strict "${chart_dir}"
    printf 'rendering %s\n' "${chart_dir#"${repo_root}/"}"
    helm template "verify-${chart_name}" "${chart_dir}" \
      --namespace worm-lab --include-crds >"${rendered}"
  done
}

run_helm_checks() {
  section 'Helm lint and render'
  local render_dir
  render_dir="$(mktemp -d)"
  trap 'rm -rf -- "${render_dir}"' RETURN
  render_charts "${render_dir}"
  run_rbac_and_policy_checks "${render_dir}"
  rm -rf -- "${render_dir}"
  trap - RETURN
}

run_rbac_and_policy_checks() {
  local render_dir="$1"
  local rendered port

  section 'RBAC and Cilium policy checks'

  # Least privilege must remain explicit: wildcard authorization is not
  # allowed in the experiment's local Helm output.
  if ! awk '
    /^kind: (Role|ClusterRole)$/ { in_role=1; next }
    /^---$/ { in_role=0 }
    in_role && /\\*/ { exit 1 }
  ' "${render_dir}"/*.yaml; then
    fail 'wildcard RBAC permission found in rendered manifests'
  fi

  # A Cilium policy is allowed to use only the four paths in the local Worm
  # model. This is deliberately a closed list, so a new network capability
  # requires an intentional verifier update.
  while IFS= read -r port; do
    case "${port}" in
      4000|8080|8081|443|53) ;;
      *) fail "Cilium policy exposes a port outside the approved graph: ${port}" ;;
    esac
  done < <(awk '
    /^kind: CiliumNetworkPolicy$/ { in_policy=1; next }
    /^---$/ { in_policy=0 }
    in_policy && /^[[:space:]]*port:[[:space:]]*/ {
      value=$0
      sub(/.*port:[[:space:]]*/, "", value)
      gsub(/"/, "", value)
      sub(/[[:space:]]*$/, "", value)
      print value
    }
  ' "${render_dir}"/*.yaml)

  # Vector is already part of the repository. Its policy must retain both
  # required halves of the path: worker -> Vector and Vector -> regression.
  rendered="${render_dir}/vector.yaml"
  [[ -f "${rendered}" ]] || fail 'Vector chart did not render'
  rg -Fq 'kind: CiliumNetworkPolicy' "${rendered}" || fail 'Vector lacks a CiliumNetworkPolicy'
  rg -Fq 'port: "8080"' "${rendered}" || fail 'Vector policy lacks worker ingress on TCP 8080'
  rg -Fq 'port: "4000"' "${rendered}" || fail 'Vector policy lacks regression egress on TCP 4000'
}

run_scaling_model() {
  section 'Bounded scaling model'
  "${script_dir}/model/check-scaling-model.sh"
}

main() {
  require_command rg
  run_go_checks
  run_elixir_checks
  run_worker_checks
  run_helm_checks
  run_scaling_model
  printf '\nverify: all checks passed\n'
}

main "$@"
