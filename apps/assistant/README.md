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

## Milestone 2 — one read-only infrastructure tool

The assistant now answers infrastructure questions by calling the Kubernetes API
**as its own ServiceAccount**, bounded by a read-only ClusterRole.

- `tools.py` — narrow, typed tools with a risk level. `get_pods()` is the first
  (level 1, read-only). `route()` maps a message to a tool (keyword rules for
  now; the Laya decision engine replaces it later, same interface).
- `chart/templates/rbac.yaml` — `ClusterRole assistant-readonly`: `get/list/watch`
  on pods, services, nodes, namespaces, events, and the apps resources; `get` on
  `pods/log`. No write verbs. Bound to the `assistant` ServiceAccount.
- Every tool call is audited to `/var/log/assistant/audit.jsonl` (JSONL on an
  `emptyDir`): `tool_call`, `tool_result`, `tool_error`.

Flow:

```
message -> route() -> get_pods() [SA token, RBAC-limited] -> data
        -> local LLM phrases the answer -> reply
```

```bash
curl -s localhost:8080/chat -H 'content-type: application/json' \
  -d '{"message":"what pods are running?"}'
# -> {"reply":"...","tool":"get_pods"}
```

Later milestones add more level-1 tools, then level-2 actions behind explicit
human confirmation, then Laya-based intent routing and voice.

## Milestone 3 — more read-only tools

Level-1 tools now include:

| Tool | Argument(s) | Use |
|---|---|---|
| `get_pods` | `namespace?` | pod status, restarts |
| `get_pod_logs` | `namespace`, `pod`, `tail_lines=100` | recent logs |
| `get_events` | `namespace?` | recent cluster events |

`route()` gained light argument extraction: a namespace from "in X"/"namespace X",
and a pod name from "pod X". If a logs question names no pod, routing returns
None and the model asks for it — deterministic failure rather than a guess.
RBAC already covers these (`get pods/log`, `get/list/watch events`); no change.

## Importing the built image into k3s (important)

`buildah` writes to its own store (`~/.local/share/containers/storage`) and
tags local images `localhost/<name>`; k3s reads its own containerd store and
resolves a bare `assistant:local` to `docker.io/library/assistant:local`. Build
without bridging and you get `ErrImageNeverPull`.

```bash
config/k3s/import-image.sh localhost/assistant:local assistant:local
sudo k3s ctr images ls | grep assistant     # expect docker.io/library/assistant:local
```
