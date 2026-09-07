# Single-command local entrypoint.
#
# Bootstrap is synchronous and happens before any Kubernetes resource is
# registered, so the fresh cluster exists before Tilt creates its Kubernetes
# client.
CONFIG_DIR = "config"
CLUSTER_CONFIG_DIR = CONFIG_DIR + "/k3d"
PLATFORM_LIFECYCLE = CLUSTER_CONFIG_DIR + "/lifecycle.sh"

local(PLATFORM_LIFECYCLE + " --once")
include("Tiltfile.workloads")
