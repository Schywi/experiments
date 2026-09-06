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
  require_text "${repo_root}/config/metallb/install.sh" 'controller.image.repository=metallb-controller' 'MetalLB controller image is not local-only'
  require_text "${repo_root}/config/metallb/install.sh" 'speaker.image.repository=metallb-speaker' 'MetalLB speaker image is not local-only'
  require_text "${repo_root}/config/metallb/install.sh" 'speaker.frr.enabled=false' 'MetalLB install unexpectedly requires FRR'
  require_text "${repo_root}/config/metallb/install.sh" 'build-and-import.sh' 'MetalLB images are not imported into k3d'
  require_text "${repo_root}/config/metallb/Dockerfile.controller" 'docker.io/golang:1.22.7' 'MetalLB controller builder is not Docker Hub addressed'
  require_text "${repo_root}/config/metallb/Dockerfile.speaker" 'docker.io/golang:1.22.7' 'MetalLB speaker builder is not Docker Hub addressed'
  require_text "${repo_root}/config/k3d/lifecycle.sh" 'K3D_CLUSTER_NAME="${cluster_name}" "${script_dir}/delete.sh"' 'Tilt lifecycle does not delete the cluster on startup'
  require_not_text "${repo_root}/config/k3d/lifecycle.sh" 'TILT_DELETE_CLUSTER_ON_EXIT' 'Tilt lifecycle contains exit-controlled deletion'
  require_text "${repo_root}/config/k3d/create.sh" '--subnet "${DOCKER_SUBNET}"' 'k3d Docker subnet is not pinned for MetalLB'
  require_text "${repo_root}/config/k3d/create.sh" '--registry-create "${REGISTRY_NAME}:0.0.0.0:${REGISTRY_PORT}"' 'k3d local registry is not created with the cluster'
  require_text "${repo_root}/config/k3d/create.sh" 'docker port "${loadbalancer}" 30080/tcp' 'k3d Hubble port check inspects the wrong container'
  require_text "${repo_root}/Tiltfile" 'local(PLATFORM_LIFECYCLE + " --once")' 'Tilt reset does not run during Tiltfile evaluation'
  require_not_text "${repo_root}/Tiltfile" 'serve_cmd=PLATFORM_LIFECYCLE' 'Tilt starts cluster reset as a long-running resource'
  require_text "${repo_root}/Tiltfile.workloads" 'deps=[CONFIG_DIR, "Tiltfile", "Tiltfile.workloads"]' 'Tilt platform resource does not depend on its inputs'
  require_text "${repo_root}/config/k3d/validate.sh" 'rollout status "${deployment}"' 'platform validation does not wait for Argo deployments'
  if rg -q --fixed-strings -- 'docker.io/local/' "${repo_root}/Tiltfile" "${repo_root}/apps" "${repo_root}/config/metallb"; then
    fail 'local application or MetalLB images use a Docker Hub-looking name'
  fi
}

validate_argocd_ordering() {
  local bootstrap_file="${repo_root}/config/k3d/bootstrap.sh"
  local cilium_line
  local argo_line
  local metallb_line
  local ingress_line
  local validate_line

  require_text "${bootstrap_file}" '"${script_dir}/install-cilium.sh"' 'bootstrap does not install Cilium'
  require_text "${bootstrap_file}" '"${repo_root}/config/argocd/install.sh"' 'bootstrap does not install Argo CD'
  require_text "${bootstrap_file}" '"${repo_root}/config/metallb/install.sh"' 'bootstrap does not install MetalLB'
  require_text "${bootstrap_file}" 'apply_argocd_ingress' 'bootstrap does not apply Argo CD Ingress'
  require_text "${bootstrap_file}" '"${script_dir}/validate.sh"' 'bootstrap does not validate the platform'

  cilium_line="$(rg -n -F '"${script_dir}/install-cilium.sh"' "${bootstrap_file}" | cut -d: -f1)"
  argo_line="$(rg -n -F '"${repo_root}/config/argocd/install.sh"' "${bootstrap_file}" | cut -d: -f1)"
  metallb_line="$(rg -n -F '"${repo_root}/config/metallb/install.sh"' "${bootstrap_file}" | cut -d: -f1)"
  ingress_line="$(rg -n '^apply_argocd_ingress$' "${bootstrap_file}" | cut -d: -f1)"
  validate_line="$(rg -n -F '"${script_dir}/validate.sh"' "${bootstrap_file}" | cut -d: -f1)"
  ((cilium_line < argo_line && argo_line < metallb_line && metallb_line < ingress_line && ingress_line < validate_line)) ||
    fail 'bootstrap stages are not strictly ordered'

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
  local workloads_tiltfile="${repo_root}/Tiltfile.workloads"
  local image_build_script="${repo_root}/scripts/build-and-import-image.sh"
  local controller_values="${repo_root}/apps/controller/chart/values.yaml"
  local regression_values="${repo_root}/apps/regression/chart/values.yaml"
  local worker_values="${repo_root}/apps/worker/chart/values.yaml"
  require_text "${controller_values}" '  repository: worm-controller' 'Worm controller is not a local image'
  require_text "${regression_values}" '  repository: worm-regression' 'Worm regression is not a local image'
  require_text "${worker_values}" '  repository: worm-worker' 'Worm worker is not a local image'
  require_text "${controller_values}" '  pullPolicy: IfNotPresent' 'Worm controller local registry pull policy is missing'
  require_text "${regression_values}" '  pullPolicy: IfNotPresent' 'Worm regression local registry pull policy is missing'
  require_text "${worker_values}" '  pullPolicy: IfNotPresent' 'Worm worker local registry pull policy is missing'
  require_text "${tiltfile}" 'local(PLATFORM_LIFECYCLE + " --once")' 'Tilt does not synchronously bootstrap the fresh cluster'
  require_text "${tiltfile}" 'include("Tiltfile.workloads")' 'Tilt workload file is not included'
  require_text "${workloads_tiltfile}" 'custom_build(' 'Worm images do not use custom builds'
  require_text "${workloads_tiltfile}" 'disable_push=True' 'Worm images are configured to push instead of import'
  require_text "${workloads_tiltfile}" 'outputs_image_ref_to=' 'Tilt image references are not returned from the importer'
  require_text "${image_build_script}" '--pull' 'Worm image builds do not refresh base images'
  require_text "${image_build_script}" '--no-cache' 'Worm image builds use the Docker layer cache'
  require_text "${repo_root}/config/k3d/import-images.sh" 'docker pull' 'platform images are not refreshed before import'
  require_text "${repo_root}/config/k3d/import-images.sh" 'k3d runtime does not contain imported image' 'platform imports are not verified in k3d'
  require_text "${repo_root}/config/metallb/build-and-import.sh" '--no-cache' 'MetalLB image builds use the Docker layer cache'
  require_text "${repo_root}/config/metallb/build-and-import.sh" 'k3d runtime does not contain imported image' 'MetalLB imports are not verified in k3d'
  require_text "${workloads_tiltfile}" 'k8s_resource("worm-controller"' 'worm-controller binding is missing'
  require_text "${workloads_tiltfile}" 'k8s_resource("regression"' 'regression binding is missing'
  require_text "${workloads_tiltfile}" 'k8s_resource("vector"' 'vector binding is missing'
  require_text "${workloads_tiltfile}" 'k8s_resource("worm-worker"' 'worm-worker binding is missing'
  require_text "${workloads_tiltfile}" 'resource_deps=["worm-controller", "regression", "vector", "worm-worker"]' 'aggregate binding is incomplete'
  if rg -n -- 'k8s_resource\("worm-(regression|vector)"' "${workloads_tiltfile}"; then
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
