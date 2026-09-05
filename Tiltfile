# Local development entrypoint for the public experiments repository.
#
# Cluster configuration and deployable manifests live below /config. Keep
# this file free of credentials and local kubeconfig references; Tilt uses
# the developer's existing Kubernetes context when resources are enabled.

CONFIG_DIR = "config"
CLUSTER_CONFIG_DIR = CONFIG_DIR + "/k3d"
GITOPS_CONFIG_DIR = CONFIG_DIR + "/argocd"

# Reload the Tilt session when the declarative configuration tree changes.
# Workloads can opt in to deployment with k8s_yaml() and k8s_resource() once
# their manifests are added under config/.
watch_file(CONFIG_DIR)

# All cluster-changing actions are manual resources. This keeps `tilt ci` and
# opening the UI side-effect free while still giving local developers one-click
# entry points for the Docker-backed, 2 GiB-capped k3d workflow.
local_resource(
    "k3d-create",
    CLUSTER_CONFIG_DIR + "/create.sh",
    deps=[CLUSTER_CONFIG_DIR + "/create.sh"],
    trigger_mode=TRIGGER_MODE_MANUAL,
    auto_init=False,
)

local_resource(
    "cilium-install",
    CLUSTER_CONFIG_DIR + "/install-cilium.sh",
    deps=[CLUSTER_CONFIG_DIR + "/install-cilium.sh", CONFIG_DIR + "/helm/cilium/values.yaml"],
    resource_deps=["k3d-create"],
    trigger_mode=TRIGGER_MODE_MANUAL,
    auto_init=False,
)

local_resource(
    "openresty-dashboard",
    "helm upgrade --install openresty " + CONFIG_DIR + "/openresty --namespace observability --create-namespace --wait",
    deps=[CONFIG_DIR + "/openresty"],
    resource_deps=["cilium-install"],
    trigger_mode=TRIGGER_MODE_MANUAL,
    auto_init=False,
)
