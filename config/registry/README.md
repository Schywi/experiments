# Rootless local image registry

A tiny `registry:2` running **inside the cluster on the node's loopback**, so a
locally-built image reaches k3s **without any `sudo`**.

## Why this exists

k3s reads its own containerd store (`/var/lib/rancher/k3s/agent/containerd`),
while `buildah`/`podman` write to the invoking user's rootless store
(`~/.local/share/containers/storage`). Nothing bridges the two, and *every*
bridge that writes into k3s is root-owned:

| Path | Owner |
|---|---|
| `/run/k3s/containerd/containerd.sock` | root — `ctr`/`crictl`/`k3s ctr` need root |
| `/var/lib/rancher/k3s/agent/images/` | root — k3s auto-import dir |

So the only way to add an image as an unprivileged user is to **not import at
all**: run a registry the node can *pull* from. This component is that registry.

```text
you (rootless)                                 k3s (already runs as root)
buildah bud && buildah push  ──────────►  127.0.0.1:5000/<name>:local  ──pull──► Pod
```

`hostNetwork: true` is what makes this work rootlessly: the registry pod binds
the **node's** loopback, so both the host's `buildah` client and k3s's
containerd reach it at `127.0.0.1:5000`. containerd talks plain HTTP to a
loopback registry, so there is **no `registries.yaml`, no TLS, and no sudo**.

## Setup (once, rootless)

```bash
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
config/registry/install.sh
```

That applies `registry.yaml` and drops a user-scoped
`~/.config/containers/registries.conf.d/local-registry.conf` marking
`127.0.0.1:5000` as plain-HTTP, so `buildah push` needs no extra flags.

## Build + push + run (per image, rootless)

```bash
buildah bud -t llama-server:local apps/models/llm
config/registry/push-image.sh llama-server        # -> 127.0.0.1:5000/llama-server:local
```

The charts reference the registry directly, e.g.
`image.repository: 127.0.0.1:5000/llama-server`, so a Pod pulls
`127.0.0.1:5000/llama-server:local` on the node — no import step.

## Notes

- The pod uses a `local-path` PVC (`local-registry-data`, 10Gi), so pushed
  images survive pod restarts. Deleting the namespace drops them.
- Re-pushing a tag overwrites it (`REGISTRY_STORAGE_DELETE_ENABLED=true`).
  Pods with `imagePullPolicy: IfNotPresent` pick the new image up on restart
  (`kubectl rollout restart`).
- `config/k3s/import-image.sh` is kept only as an **offline fallback** (no
  registry); it is the one path that still needs a single `sudo` line.
