# Assistant (Milestone 1)

The text-first assistant loop. It exposes `POST /chat` over a `ClusterIP`
Service and forwards each turn to the local LLM (`apps/models/llm`). No tools,
no Kubernetes API, no voice yet — the smallest useful system, so the
intelligence loop can be measured and debugged on its own.

```text
apps/assistant/
├── Containerfile        # python:3.12-slim + fastapi/uvicorn/httpx
├── server.py            # POST /chat -> llama.cpp /v1/chat/completions
├── chat.sh              # host CLI: port-forward + one message
└── chart/               # Deployment + ClusterIP Service + ServiceAccount
```

## Flow

```
chat.sh --port-forward--> svc/assistant:8080 --> pod(assistant)
                                                    └--> http://llm.models.svc:8000/v1/chat/completions
```

## Prerequisites

- `apps/models/llm` is deployed and serving (`svc/llm` in namespace `models`).
  The default `LLM_URL` is `http://llm.models.svc.cluster.local:8000`.

## Build and import (on the host)

```bash
buildah bud -t assistant:local apps/assistant
buildah push assistant:local oci-archive:/tmp/assistant.tar
sudo k3s ctr images import /tmp/assistant.tar
```

## Deploy

```bash
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
helm upgrade --install assistant apps/assistant/chart -n assistant --create-namespace
```

## Use

```bash
apps/assistant/chat.sh "explain what a Kubernetes pod is in one sentence"
```

## Notes / limits

- **Stateless**: each call is one turn; no conversation history yet (later
  milestone).
- **No identity**: `automountServiceAccountToken: false` — it does not touch the
  Kubernetes API. The read-only ServiceAccount/RBAC boundary is added at
  Milestone 2 with the first infrastructure tool.
- Latency is CPU-bound by the 1.5B LLM (~10–20 tok/s).
