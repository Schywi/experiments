# Local development entrypoint for the public experiments repository.
#
# Tilt drives the k3d lifecycle without destroying an existing cluster. Stop
# Tilt to preserve the cluster; set TILT_DELETE_CLUSTER_ON_EXIT=true only when
# an explicit teardown is intended.

CONFIG_DIR = "config"
CLUSTER_CONFIG_DIR = CONFIG_DIR + "/k3d"
METALLB_CONFIG_DIR = CONFIG_DIR + "/metallb"
PLATFORM_LIFECYCLE = CLUSTER_CONFIG_DIR + "/lifecycle.sh"
watch_file(CONFIG_DIR)

# lifecycle.sh owns the complete platform gate: k3d, Cilium, MetalLB,
# LoadBalancer ingress, Argo CD, and HTTP validation. Workloads below depend on
# this resource and cannot be applied until that gate succeeds.
local_resource(
    "local-platform",
    serve_cmd=PLATFORM_LIFECYCLE,
    deps=[CONFIG_DIR, METALLB_CONFIG_DIR, "Tiltfile"],
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
k8s_resource("regression", resource_deps=["worm-controller"])

k8s_yaml(helm("config/vector", name="worm-vector", namespace=WORM_NAMESPACE))
k8s_resource("vector", resource_deps=["regression"])

k8s_yaml(helm("apps/worker/chart", name="worm-worker", namespace=WORM_NAMESPACE))
k8s_resource(
    "worm-worker",
    resource_deps=["worm-controller", "vector"],
)

# This is a namespace-level visibility resource, not a second deployment
# controller. It gives the local experiment a stable entry in the Tilt UI.
local_resource(
    "worm-lab",
    cmd="true",
    resource_deps=["worm-controller", "regression", "vector", "worm-worker"],
)
