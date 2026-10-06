# Small LLM (CPU)

`llama.cpp`'s `llama-server` serving a 1.5B GGUF chat model, CPU-only. The
OpenAI-compatible API is at `/v1/chat/completions`.

```text
apps/models/llm/
├── Containerfile        # multi-stage llama.cpp build -> slim runtime
└── chart/               # Deployment + ClusterIP Service + weights PVC
```

## Model

Default: **Qwen2.5-1.5B-Instruct**, `Q4_K_M` (from `bartowski/...-GGUF`).
~0.94 GiB of weights, ~1.5 GiB resident, ≈10–20 tok/s on this 6-core CPU.
Swap `model.repo`/`model.file` in `values.yaml` for a 3B (≈4–8 tok/s) if wanted.

## Build and import (on the host)

```bash
buildah bud -t llama-server:local apps/models/llm
buildah push llama-server:local oci-archive:/tmp/llama-server.tar
sudo k3s ctr images import /tmp/llama-server.tar
```

## Deploy

```bash
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
kubectl create namespace models
helm upgrade --install llm apps/models/llm/chart -n models
```

The init container downloads the GGUF onto the `llm-models` PVC.

## Call it

```bash
kubectl -n models port-forward svc/llm 8000:8000
curl -s localhost:8000/v1/chat/completions -H 'content-type: application/json' -d '{
  "messages": [{"role": "user", "content": "Say hello in one short sentence."}],
  "max_tokens": 64
}'
```

## Notes

- Built with `-DGGML_NATIVE=ON` (AVX2 for this host). The GPU/Vulkan upgrade is a
  Track C follow-on; this image is CPU only.
- `model.threads` should stay at or below the physical core count (6).
- Context is capped at 4096 by default to bound RAM on a 16 GiB machine shared
  with the desktop.
