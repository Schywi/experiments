# Laya (CPU decision engine)

Laya served over its Jev-compatible HTTP API, on the single native-k3s node.
CPU-only: the GPU fast path is CUDA/TileLang, which this host's AMD RX 570
cannot use.

```text
apps/models/laya/
├── Containerfile        # python:3.12-slim + CPU torch + laya[serve]
└── chart/               # Deployment + ClusterIP Service + weights PVC
```

## Build and import (on the host)

k3s cannot `docker build`; use buildah and import into containerd:

```bash
buildah bud -t laya-serve:local apps/models/laya
buildah push laya-serve:local oci-archive:/tmp/laya-serve.tar
sudo k3s ctr images import /tmp/laya-serve.tar
```

## Deploy

```bash
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
kubectl create namespace models
helm upgrade --install laya apps/models/laya/chart -n models
```

The init container primes the Hugging Face cache onto the `laya-models` PVC;
the server then runs offline (`HF_HUB_OFFLINE=1`).

## Call it

```bash
kubectl -n models port-forward svc/laya 8000:8000
curl -s localhost:8000/v1/systemone -H 'content-type: application/json' -d '{
  "state": {"body": "billed twice, refund please or we cancel"},
  "questions": {"dept": {"type": "choice", "instructions": "which team?",
                "criteria": {"billing": "refunds", "tech": "bugs"}}}
}'
```

## Notes

- RAM: ~2–3 GiB with all three checkpoints preloaded; the chart requests 2 GiB.
- Latency on this CPU: ~200–460 ms per decision.
- `laya-serve` preloads english + multilingual by default; set `serve.preload` to
  control this. Keep `serve.threads` at or below the physical core count (6).
