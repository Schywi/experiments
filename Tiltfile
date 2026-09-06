# Local development entrypoint for the public experiments repository.
#
# Tilt owns the complete k3d lifecycle. Starting this Tiltfile creates a fresh
# cluster; stopping Tilt removes that cluster and its Docker resources.

CONFIG_DIR = "config"
CLUSTER_CONFIG_DIR = CONFIG_DIR + "/k3d"
watch_file(CONFIG_DIR)

local_resource(
    "local-platform",
    serve_cmd=CLUSTER_CONFIG_DIR + "/lifecycle.sh",
    deps=[CONFIG_DIR, "Tiltfile"],
    auto_init=True,
)

# tilt file need REWRITE

# ---------------------------------------------------------------------------
# Bounded Worm local-development extension
#
# This block is intentionally additive.  The platform resource above remains
# untouched; these workloads are local-only and are never Argo CD Applications.
# ---------------------------------------------------------------------------

WORM_NAMESPACE = "worm-lab"

custom_build(
    "docker.io/local/worm-controller:tilt",
    "apps/controller/build-and-import.sh \"$EXPECTED_REF\"",
    deps=["apps/controller"],
    disable_push=True,
)

custom_build(
    "docker.io/local/worm-regression:tilt",
    "apps/regression/build-and-import.sh \"$EXPECTED_REF\"",
    deps=["apps/regression"],
    disable_push=True,
)

custom_build(
    "docker.io/local/worm-worker:tilt",
    "apps/worker/build-and-import.sh \"$EXPECTED_REF\"",
    deps=["apps/worker"],
    disable_push=True,
)

k8s_yaml(helm("apps/controller/chart", name="worm-controller", namespace=WORM_NAMESPACE))
k8s_resource("worm-controller", resource_deps=["local-platform"])

k8s_yaml(helm("apps/regression/chart", name="worm-regression", namespace=WORM_NAMESPACE))
k8s_resource("worm-regression", resource_deps=["worm-controller"])

k8s_yaml(helm("config/vector", name="worm-vector", namespace=WORM_NAMESPACE))
k8s_resource("worm-vector", resource_deps=["worm-regression"])

k8s_yaml(helm("apps/worker/chart", name="worm-worker", namespace=WORM_NAMESPACE))
k8s_resource(
    "worm-worker",
    resource_deps=["worm-controller", "worm-vector"],
)

# This is a namespace-level visibility resource, not a second deployment
# controller. It gives the local experiment a stable entry in the Tilt UI.
local_resource(
    "worm-lab",
    cmd="true",
    resource_deps=["worm-controller", "worm-regression", "worm-vector"],
)
