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

## Build + import (on the host, once per app)

k3s cannot `docker build`. Use `buildah` (rootless) and import into containerd:

```bash
sudo dnf install -y buildah
for app in laya kokoro llm; do
  tag=$( [ "$app" = laya ] && echo laya-serve   \
       || { [ "$app" = kokoro ] && echo kokoro-tts || echo llama-server; } )
  buildah bud -t "${tag}:local" "apps/models/${app}"
  buildah push "${tag}:local" "oci-archive:/tmp/${tag}.tar"
  sudo k3s ctr images import "/tmp/${tag}.tar"
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
- **Not yet built or deployed** — the images require `buildah` on the host, and
  the DNS fix requires a host `k3s` restart. Neither is available to the agent
  sandbox.

## Importing built images into k3s (important)

`buildah`/`podman` and k3s use **different image stores**. Building an image
does not put it in the cluster. `buildah` also tags local images as
`localhost/<name>`, while k3s resolves a bare `<name>:<tag>` to
`docker.io/library/<name>:<tag>` — so an import under the wrong name looks like
`ErrImageNeverPull`.

Use the helper, which retags, exports, imports, and verifies:

```bash
config/k3s/import-image.sh localhost/laya-serve:local   laya-serve:local
config/k3s/import-image.sh localhost/kokoro-tts:local   kokoro-tts:local
config/k3s/import-image.sh localhost/llama-server:local llama-server:local
```

Then confirm the names k3s actually has:

```bash
sudo k3s ctr images ls | grep -E 'laya|kokoro|llama'
```
