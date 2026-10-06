# Model services (CPU)

CPU model services for the native-k3s single-node cluster. All three are
CPU-only by design: this host's GPU is an AMD RX 570 (gfx803), which has no
ROCm/CUDA path, so the GPU fast paths these runtimes offer are unusable here.
See `research/native-k3s-gpu-models-plan.md` for the full rationale.

```text
apps/models/
├── laya/    # decision engine        -> POST /v1/systemone        (port 8000)
├── kokoro/  # Kokoro-82M TTS         -> POST /v1/audio/speech     (port 8000)
└── llm/     # llama.cpp + 1.5B GGUF  -> POST /v1/chat/completions (port 8000)
```

Each app is a `Containerfile` + a Helm `chart/` with the same shape: a
`Deployment` whose **init container downloads weights onto a `local-path` PVC**,
a `ClusterIP` `Service`, and the `PVC`. The server then runs offline from the
cache.

## Prerequisite: cluster DNS

The init containers fetch weights from the internet, so Pods must resolve
external names. The host carries `search mygateway`, which leaks into every
Pod and makes external resolution stall (details in
`../../config/k3s/resolv.conf`). Apply the cluster-only fix **once**, then
restart k3s:

```bash
cd <repo>
sudo bash config/k3s/install-k3s.sh      # installs the clean resolv.conf
# (k3s restarts; verify with the smoke pod in config/k3s/validate.sh)
```

## Build + push (on the host, once per app, rootless)

k3s cannot `docker build`. Use `buildah` (rootless) and push into the
in-cluster registry (see `config/registry/`) — **no `sudo`**:

```bash
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
config/registry/install.sh                 # once: starts the registry + client config
for app in laya kokoro llm; do
  tag=$( [ "$app" = laya ] && echo laya-serve   \
       || { [ "$app" = kokoro ] && echo kokoro-tts || echo llama-server; } )
  buildah bud -t "${tag}:local" "apps/models/${app}"
  config/registry/push-image.sh "${tag}"   # -> 127.0.0.1:5000/${tag}:local
done
```

The LLM image compiles `llama.cpp` from source (a few minutes); the others are
pip installs.

## Deploy

```bash
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
kubectl create namespace models
for app in laya kokoro llm; do
  helm upgrade --install "$app" "apps/models/$app/chart" -n models
done
kubectl -n models get pods,pvc
```

## Memory budget (this 16 GiB host)

| Component | Resident |
|---|---|
| platform (k3s + Cilium + Argo CD + Hubble) | ~2–3 GiB |
| laya (3 checkpoints preloaded) | ~2–3 GiB |
| kokoro | ~0.6 GiB |
| llm (Qwen2.5-1.5B Q4_K_M) | ~1.5 GiB |
| **models total** | **~5 GiB** |

Fits alongside the desktop, but the row for `llm` grows fast with a 3B model
(~3.5 GiB) — raise `model.contextSize` only deliberately.

## Verify

```bash
# each service, via port-forward
kubectl -n models port-forward svc/laya   8000:8000   # then curl /v1/systemone
kubectl -n models port-forward svc/kokoro 8000:8000   # then curl /v1/audio/speech
kubectl -n models port-forward svc/llm    8000:8000   # then curl /v1/chat/completions
```

Then open **http://localhost:30080/** (Hubble UI), select the `models` namespace,
and generate a request — you should see the port-forward/service flows.

## Status

- Charts are linted and render (see each app's README).
- Images are built with `buildah` and pushed to the rootless in-cluster
  registry (`config/registry/`) — no `sudo`, no containerd import step.

## How the images reach k3s (important)

`buildah`/`podman` and k3s use **different image stores**, and every path that
writes into k3s's containerd is root-owned. Rather than import (which needs
`sudo`), the images are **pulled from a local registry** the node can reach:

```text
buildah push  ──►  127.0.0.1:5000/<name>:local  ──pull──►  Pod
```

`config/registry/` runs that registry (a `registry:2` bound to the node's
loopback) and installs the user-scoped client config. Push with no `sudo`:

```bash
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
config/registry/install.sh
config/registry/push-image.sh laya-serve
config/registry/push-image.sh kokoro-tts
config/registry/push-image.sh llama-server
```

The charts reference `127.0.0.1:5000/<name>`, so no import step and no
privileged access are needed. `config/k3s/import-image.sh` remains as an
offline fallback (that single path still needs one `sudo` line).
