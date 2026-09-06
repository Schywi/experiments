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
