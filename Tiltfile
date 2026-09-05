# Local development entrypoint for the public experiments repository.
#
# Cluster configuration and deployable manifests live below /config. Keep
# this file free of credentials and local kubeconfig references; Tilt uses
# the developer's existing Kubernetes context when resources are enabled.

CONFIG_DIR = "config"
CLUSTER_CONFIG_DIR = CONFIG_DIR + "/cluster"
GITOPS_CONFIG_DIR = CONFIG_DIR + "/gitops"

# Reload the Tilt session when the declarative configuration tree changes.
# Workloads can opt in to deployment with k8s_yaml() and k8s_resource() once
# their manifests are added under config/.
watch_file(CONFIG_DIR)

# Keep the root Tiltfile executable before the first workload is introduced.
# This placeholder gives CI and local users a stable resource to select while
# remaining side-effect free and secret-free.
local_resource(
    "config-layout",
    "true",
    deps=[CONFIG_DIR],
    trigger_mode=TRIGGER_MODE_MANUAL,
    auto_init=False,
)
