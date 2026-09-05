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

# `platform-bootstrap` is an explicit aggregate/debug path. The default
# `tilt up` path below initializes each platform branch independently after
# k3d creation, so OpenResty and Argo CD do not wait for Cilium validation.
local_resource(
    "platform-bootstrap",
    CLUSTER_CONFIG_DIR + "/bootstrap.sh",
    deps=[
        CLUSTER_CONFIG_DIR + "/bootstrap.sh",
        CLUSTER_CONFIG_DIR + "/create.sh",
        CLUSTER_CONFIG_DIR + "/import-images.sh",
        CLUSTER_CONFIG_DIR + "/install-cilium.sh",
        CLUSTER_CONFIG_DIR + "/validate.sh",
        CONFIG_DIR + "/helm/cilium/values.yaml",
        CONFIG_DIR + "/openresty",
        CONFIG_DIR + "/argocd",
    ],
    auto_init=False,
)

local_resource(
    "k3d-create",
    CLUSTER_CONFIG_DIR + "/create.sh",
    deps=[CLUSTER_CONFIG_DIR + "/create.sh"],
    auto_init=True,
)

local_resource(
    "k3d-import-images",
    CLUSTER_CONFIG_DIR + "/import-images.sh",
    deps=[CLUSTER_CONFIG_DIR + "/import-images.sh"],
    resource_deps=["k3d-create"],
    auto_init=True,
)

local_resource(
    "cilium-install",
    CLUSTER_CONFIG_DIR + "/install-cilium.sh",
    deps=[CLUSTER_CONFIG_DIR + "/install-cilium.sh", CONFIG_DIR + "/helm/cilium/values.yaml"],
    resource_deps=["k3d-import-images"],
    auto_init=True,
)

local_resource(
    "cilium-validate",
    CLUSTER_CONFIG_DIR + "/validate.sh",
    deps=[CLUSTER_CONFIG_DIR + "/validate.sh"],
    resource_deps=["cilium-install"],
    auto_init=True,
)

local_resource(
    "openresty-dashboard",
    "helm upgrade --install cilium-dashboard " + CONFIG_DIR + "/openresty --namespace openresty --create-namespace --wait",
    deps=[CONFIG_DIR + "/openresty"],
    resource_deps=["k3d-create"],
    auto_init=True,
)

local_resource(
    "argocd-install",
    CONFIG_DIR + "/argocd/install.sh",
    deps=[CONFIG_DIR + "/argocd/install.sh", CONFIG_DIR + "/argocd/values.yaml"],
    resource_deps=["k3d-create"],
    auto_init=True,
)

local_resource(
    "argocd-application",
    "kubectl apply --filename " + CONFIG_DIR + "/argocd/applications/openresty.yaml",
    deps=[CONFIG_DIR + "/argocd/applications/openresty.yaml"],
    resource_deps=["openresty-dashboard", "argocd-install"],
    auto_init=True,
)
