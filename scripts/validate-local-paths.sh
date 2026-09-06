#!/usr/bin/env bash
# Static validation for the local Cilium/Hubble ingress path and Tilt bindings.
# This script does not contact Kubernetes, Docker, a registry, or Tilt.
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "${script_dir}/.." && pwd)"

fail() {
  printf 'validate-local-paths: %s\n' "$*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "required command is unavailable: $1"
}

require_text() {
  local file="$1" pattern="$2" description="$3"
  rg -q --fixed-strings -- "${pattern}" "${file}" || fail "${description} (${file})"
}

require_not_text() {
  local file="$1" pattern="$2" description="$3"
  if rg -q --fixed-strings -- "${pattern}" "${file}"; then
    fail "${description} (${file})"
  fi
}

validate_hubble_path() {
  local values_file="${repo_root}/config/helm/cilium/values.yaml"
  local ingress_file="${repo_root}/config/helm/cilium/hubble-ui-ingress.yaml"
  local k3d_file="${repo_root}/config/k3d/create.sh"

  require_text "${values_file}" '  enabled: true' 'Cilium ingress controller is disabled'
  require_text "${values_file}" '  loadbalancerMode: shared' 'Cilium ingress mode is not shared'
  require_text "${values_file}" '    type: NodePort' 'Cilium ingress service is not NodePort'
  require_text "${values_file}" '    externalTrafficPolicy: Cluster' 'Cilium ingress external traffic policy is not Cluster'
  require_text "${values_file}" '    insecureNodePort: 30080' 'Cilium ingress HTTP NodePort is not pinned'
  require_text "${ingress_file}" 'kind: Ingress' 'Hubble manifest is not an Ingress'
  require_text "${ingress_file}" '  namespace: kube-system' 'Hubble Ingress is not in kube-system'
  require_text "${ingress_file}" '    ingress.cilium.io/loadbalancer-mode: shared' 'Hubble Ingress is not shared'
  require_text "${ingress_file}" '  ingressClassName: cilium' 'Hubble Ingress does not select Cilium'
  require_text "${ingress_file}" '    - host: localhost' 'Hubble Ingress host is not localhost'
  require_text "${ingress_file}" '                name: hubble-ui' 'Hubble backend is not hubble-ui'
  require_text "${ingress_file}" '                  number: 80' 'Hubble backend port is not 80'
  require_text "${k3d_file}" '127.0.0.1:8080:30080@server:0' 'k3d localhost mapping is missing'
}

validate_metallb_path() {
  local pool_file="${repo_root}/config/metallb/address-pool.yaml"
  local overlay_file="${repo_root}/config/metallb/cilium-values.yaml"
  local kustomization_file="${repo_root}/config/metallb/kustomization.yaml"

  require_text "${pool_file}" 'kind: IPAddressPool' 'MetalLB IPAddressPool is missing'
  require_text "${pool_file}" '    - 172.20.0.240-172.20.0.250' 'MetalLB Docker-host pool is incorrect'
  require_text "${pool_file}" 'kind: L2Advertisement' 'MetalLB L2Advertisement is missing'
  require_text "${pool_file}" '        kubernetes.io/hostname: k3d-cilium-lab-server-0' 'MetalLB node selector is incorrect'
  require_text "${pool_file}" '    - eth0' 'MetalLB interface is not pinned to the k3d node'
  require_text "${kustomization_file}" '  - address-pool.yaml' 'MetalLB Kustomization omits address-pool.yaml'
  require_text "${overlay_file}" '    type: LoadBalancer' 'MetalLB Cilium overlay does not use LoadBalancer'
  require_text "${overlay_file}" '      metallb.io/address-pool: docker-host-pool' 'MetalLB Cilium overlay does not select the pool'
  require_text "${repo_root}/config/k3d/bootstrap.sh" 'config/metallb/install.sh' 'bootstrap does not install MetalLB'
  require_text "${repo_root}/config/k3d/bootstrap.sh" 'config/metallb/configure.sh' 'bootstrap does not apply MetalLB configuration'
  require_text "${repo_root}/config/k3d/bootstrap.sh" 'enable-metallb-ingress.sh' 'bootstrap does not enable the MetalLB Cilium overlay'
  require_text "${repo_root}/config/metallb/install.sh" 'docker.io/local/metallb-controller' 'MetalLB controller image is not local and Docker Hub-addressed'
  require_text "${repo_root}/config/metallb/install.sh" 'docker.io/local/metallb-speaker' 'MetalLB speaker image is not local and Docker Hub-addressed'
  require_text "${repo_root}/config/metallb/install.sh" 'speaker.frr.enabled=false' 'MetalLB install unexpectedly requires FRR'
  require_text "${repo_root}/config/metallb/install.sh" 'build-and-import.sh' 'MetalLB images are not imported into k3d'
  require_text "${repo_root}/config/metallb/Dockerfile.controller" 'docker.io/golang:1.22.7' 'MetalLB controller builder is not Docker Hub addressed'
  require_text "${repo_root}/config/metallb/Dockerfile.speaker" 'docker.io/golang:1.22.7' 'MetalLB speaker builder is not Docker Hub addressed'
  require_not_text "${repo_root}/config/k3d/lifecycle.sh" '"${script_dir}/delete.sh"' 'Tilt lifecycle deletes the cluster on startup'
  require_text "${repo_root}/config/k3d/create.sh" '--subnet "${DOCKER_SUBNET}"' 'k3d Docker subnet is not pinned for MetalLB'
  require_text "${repo_root}/Tiltfile" 'METALLB_CONFIG_DIR = CONFIG_DIR + "/metallb"' 'Tiltfile does not include MetalLB configuration'
  require_text "${repo_root}/Tiltfile" 'deps=[CONFIG_DIR, METALLB_CONFIG_DIR, "Tiltfile"]' 'Tilt platform resource does not depend on MetalLB configuration'
  require_text "${repo_root}/config/k3d/validate.sh" 'rollout status "${deployment}"' 'platform validation does not wait for Argo deployments'
}

validate_argocd_ordering() {
  local bootstrap_file="${repo_root}/config/k3d/bootstrap.sh"
  local argo_wait_line
  local cilium_wait_line
  local argo_apply_call_line

  argo_wait_line="$(rg -n 'if wait "\$\{argocd_pid\}"' "${bootstrap_file}" | cut -d: -f1)"
  cilium_wait_line="$(rg -n 'if wait "\$\{cilium_pid\}"' "${bootstrap_file}" | cut -d: -f1)"
  argo_apply_call_line="$(rg -n 'if ! apply_argocd_ingress' "${bootstrap_file}" | cut -d: -f1)"
  [[ -n "${argo_wait_line}" && -n "${cilium_wait_line}" && -n "${argo_apply_call_line}" ]] ||
    fail 'bootstrap ordering markers are incomplete'
  ((argo_wait_line < argo_apply_call_line)) || fail 'Argo Ingress is applied before Argo Helm wait'
  ((cilium_wait_line < argo_apply_call_line)) || fail 'Argo Ingress is applied before Cilium wait'

  if rg -q 'argocd_ingress_file|argocd-ingress.yaml' "${repo_root}/config/k3d/install-cilium.sh"; then
    fail 'Cilium installer still owns the Argo Ingress'
  fi
}

render_and_check() {
  local chart_dir="$1" expected_kind="$2" expected_name="$3" rendered
  rendered="$(helm template "validate-$(basename -- "$(dirname -- "${chart_dir}")")" "${chart_dir}" --namespace worm-lab)" ||
    fail "Helm render failed: ${chart_dir}"
  printf '%s\n' "${rendered}" |
    rg -Uq -- "kind: ${expected_kind}\\nmetadata:\\n  name: ${expected_name}\\n" ||
    fail "${chart_dir} does not render ${expected_kind}/${expected_name}"
}

validate_worm_bindings() {
  local tiltfile="${repo_root}/Tiltfile"
  require_text "${tiltfile}" 'k8s_resource("worm-controller"' 'worm-controller binding is missing'
  require_text "${tiltfile}" 'k8s_resource("regression"' 'regression binding is missing'
  require_text "${tiltfile}" 'k8s_resource("vector"' 'vector binding is missing'
  require_text "${tiltfile}" '    "worm-worker",' 'worm-worker binding is missing'
  require_text "${tiltfile}" '    resource_deps=["worm-controller", "regression", "vector", "worm-worker"],' 'aggregate binding is incomplete'
  if rg -n -- 'k8s_resource\("worm-(regression|vector)"' "${tiltfile}"; then
    fail 'Tilt binds Helm release names instead of rendered regression/vector names'
  fi
  render_and_check "${repo_root}/apps/controller/chart" Deployment worm-controller
  render_and_check "${repo_root}/apps/regression/chart" Deployment regression
  render_and_check "${repo_root}/config/vector" Deployment vector
  render_and_check "${repo_root}/apps/worker/chart" Deployment worm-worker
}

main() {
  require_command helm
  require_command rg
  validate_hubble_path
  validate_metallb_path
  validate_argocd_ordering
  validate_worm_bindings
  printf 'validate-local-paths: all checks passed\n'
}

main "$@"
