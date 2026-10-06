# AI workflow — local infrastructure assistant ("Jarvis")

Repo-visible map of how the local assistant is **built, shipped, and run** on the
single-node native k3s cluster. The long-form charter (the mentor prompt), the
current-state audit, and the gap list live in `docs/assistant-plan.md` — which is
**local-only** (`docs/` is gitignored). This file is the tracked entry point.

## What it is

A private assistant that answers questions about this infrastructure: text in,
text out (voice later). A local LLM provides the language; **read-only,
narrowly-scoped tools** provide the facts; there is no arbitrary shell — the
tools call the Kubernetes API as the Pod's own ServiceAccount.

## Components

| Area | Path | Notes |
|---|---|---|
| Platform | `config/k3s/` | native k3s + Cilium + Hubble + Argo CD |
| Observability | `config/victoriametrics/`, `config/grafana/` | metrics stack |
| Image registry | `config/registry/` | rootless, `127.0.0.1:5000` |
| Models | `apps/models/{laya,kokoro,llm}/` | CPU; each = Containerfile + Helm chart |
| GPU | `config/gpu/`, `apps/gpu-device-plugin/` | Vulkan works; no ROCm |
| Assistant | `apps/assistant/` | **on branch `trackc` only** |
| GitOps | `config/argocd/applications/` | Argo CD Applications |

## The delivery workflow (the part that bites)

```text
you (rootless)                                  k3s (runs as root)
buildah bud && buildah push ─────────► 127.0.0.1:5000/<name>:local ──pull──► Pod
```

`buildah`/`podman` write to `~/.local/share/containers/storage`; k3s reads its
own containerd store. Every bridge that writes **into** k3s is root-owned
(`containerd.sock`, `/var/lib/rancher/k3s/agent/images/`), so as an unprivileged
user the answer is to **not import at all** — run a registry the node can pull
from (`config/registry/`, a `hostNetwork` `registry:2` on the node loopback).

```bash
config/registry/install.sh                    # once
buildah bud -t llama-server:local apps/models/llm
config/registry/push-image.sh llama-server     # -> 127.0.0.1:5000/llama-server:local
kubectl -n models rollout restart deploy/llm   # pick up the tag
```

Gotchas (each seen in practice):

- `buildah` tags a local image `localhost/<name>:local`; the charts reference
  `127.0.0.1:5000/<name>:local`. The registery ref must match the chart exactly.
- Building is not shipping — a tag that was never **pushed** leaves Pods in
  `ImagePullBackOff` even though the registry is healthy.
- `config/k3s/import-image.sh` is the **offline fallback** (no registry); it is
  the one path that still needs a single `sudo`.

## Tracks and milestones

| Track | State |
|---|---|
| A — platform (k3s + Cilium + Hubble + Argo) | done; pod-DNS fix pending apply |
| B — CPU models (Laya / Kokoro / LLM) | charts done; deploy pending |
| C — GPU (Vulkan YES/NO + Go adapter) | Vulkan confirmed on the RX 570; device-cgroup note |

Assistant milestones (charter M1–M3): M1 text loop; M2 `get_pods` + read-only
RBAC + audit; M3 `get_pod_logs` + `get_events`.

## Current state and gotchas

- **Models are deployed but not running:** Pods are in `ImagePullBackOff` because
  the images were built but never **pushed** to `127.0.0.1:5000`. Fix with
  `config/registry/push-image.sh`.
- **Branch split (top structural risk):** the assistant lives on `trackc`; the
  observability stack lives on `native-k3s-gpu-models`. Neither branch has both.
  Reconcile before deploying the assistant.
- **Pod DNS:** the host's `search mygateway` leaks into Pods and breaks external
  resolution; `config/k3s/resolv.conf` fixes it but needs `install-k3s.sh` re-run.

## Where the details live

- `docs/assistant-plan.md` — charter + status + gaps (**local-only**, gitignored)
- `research/native-k3s-gpu-models-plan.md` — tracks and recorded decisions
- `config/registry/README.md`, `config/k3s/README.md`, `apps/models/README.md`,
  `apps/assistant/README.md`, `apps/gpu-device-plugin/README.md`,
  `config/gpu/README.md`
