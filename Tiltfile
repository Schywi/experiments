# Local development entrypoint for the public experiments repository.
#
# Tilt owns the local k3d lifecycle. Every `tilt up` starts from a clean
# cluster by running config/k3d/delete.sh before bootstrap.

CONFIG_DIR = "config"
CLUSTER_CONFIG_DIR = CONFIG_DIR + "/k3d"
METALLB_CONFIG_DIR = CONFIG_DIR + "/metallb"
PLATFORM_LIFECYCLE = CLUSTER_CONFIG_DIR + "/lifecycle.sh"
LOCAL_REGISTRY = "k3d-cilium-lab-registry.localhost:5000"
default_registry(LOCAL_REGISTRY)
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

docker_build("worm-controller", "apps/controller", dockerfile="apps/controller/Containerfile")
docker_build("worm-regression", "apps/regression", dockerfile="apps/regression/Containerfile")
docker_build("worm-worker", "apps/worker", dockerfile="apps/worker/Containerfile")

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
