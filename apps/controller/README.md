# Worm controller

The controller provides the bounded reproduction control plane. It watches a
namespaced `Worm`, reconciles its named worker Deployment, and exposes only:

`POST /v1/replication-intents`

```json
{"worm_id":"worker-pod-uid","intent_id":"unique-retry-safe-id"}
```

`WORM_NAMESPACE` and `WORM_NAME` select the single Worm accepted by this
controller instance. New intent IDs increment `status.desiredReplicas` up to
`spec.maxReplicas`; repeated intent IDs return the prior desired count.

The controller needs RBAC for `worms`, `worms/status`, and only the target
Deployment's `deployments/scale` subresource when deployed.

## Local k3d image

Tilt supplies a content-addressed image reference to the local build script.
The script builds it locally and imports it into the existing `cilium-lab`
cluster; it never pushes an image to a remote registry.

```sh
./build-and-import.sh docker.io/local/worm-controller:dev
```
