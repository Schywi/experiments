#!/usr/bin/env bash

set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "${script_dir}/../.." && pwd)"
helm_bin="${HELM_BIN:-helm}"
kubeconform_bin="${KUBECONFORM_BIN:-kubeconform}"

# Keep the schema repository immutable in CI. The selected version is old
# enough to cover the public k3s baseline while still containing all standard
# APIs used by the platform charts.
kubernetes_schema_version="${KUBERNETES_SCHEMA_VERSION:-v1.31.0}"
schema_location="https://raw.githubusercontent.com/yannh/kubernetes-json-schema/master/${kubernetes_schema_version}-standalone-strict/{{.ResourceKind}}{{.KindSuffix}}.json"
charts_root="${repo_root}/config"

command -v "${helm_bin}" >/dev/null 2>&1 || {
  echo "helm is required" >&2
  exit 1
}

command -v "${kubeconform_bin}" >/dev/null 2>&1 || {
  echo "kubeconform is required" >&2
  exit 1
}

mapfile -d '' charts < <(find "${charts_root}" -type f -name Chart.yaml -print0 | sort -z)

if ((${#charts[@]} == 0)); then
  echo "No Helm charts found below ${charts_root}; validation will run when a chart is added."
  exit 0
fi

for chart_file in "${charts[@]}"; do
  chart_dir="$(dirname -- "${chart_file}")"
  chart_name="$(basename -- "${chart_dir}")"
  rendered_manifest="$(mktemp --suffix=.yaml)"
  trap 'rm -f -- "${rendered_manifest}"' RETURN

  echo "Linting Helm chart: ${chart_dir#"${repo_root}"/}"
  "${helm_bin}" lint --strict "${chart_dir}"

  echo "Rendering Helm chart: ${chart_name}"
  "${helm_bin}" template "ci-${chart_name}" "${chart_dir}" \
    --namespace "ci-${chart_name}" \
    --include-crds >"${rendered_manifest}"

  echo "Validating rendered manifests: ${chart_name}"
  "${kubeconform_bin}" \
    -strict \
    -summary \
    -ignore-missing-schemas \
    -schema-location "${schema_location}" \
    "${rendered_manifest}"

  rm -f -- "${rendered_manifest}"
  trap - RETURN
done

# Validate the pinned remote Cilium chart with the same values used by the
# k3d bootstrap. This catches chart/value drift that local charts cannot see.
cilium_version="${CILIUM_VERSION:-1.13.4}"
cilium_values="${repo_root}/config/helm/cilium/values.yaml"

echo "Rendering remote Cilium chart: ${cilium_version}"
"${helm_bin}" repo add cilium https://helm.cilium.io --force-update
"${helm_bin}" repo update cilium
"${helm_bin}" template ci-cilium cilium/cilium \
  --namespace kube-system \
  --version "${cilium_version}" \
  --values "${cilium_values}" \
  --set-string k8sServiceHost=127.0.0.1 \
  --set-string k8sServicePort=6443 \
  --include-crds |
  "${kubeconform_bin}" \
    -strict \
    -summary \
    -ignore-missing-schemas \
    -schema-location "${schema_location}" \
    -
