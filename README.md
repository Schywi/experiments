# experiments

A single-host Kubernetes lab on a Fedora desktop. It started as a bounded
self-replication experiment on k3d and now runs native k3s with Cilium, three
local models, a GPU device plugin for an AMD RX 570, and an assistant that
answers questions about the cluster.

Everything documented here was measured on this one machine. Numbers and
addresses are the ones the cluster actually reports.

## Host

| | |
|---|---|
| OS / kernel | Fedora 44 Workstation, kernel 6.19 |
| CPU | Intel i5-9400F, 6 cores / 6 threads, AVX2 only |
| RAM | 15 GiB, plus 8 GiB zram swap |
| GPU | AMD Radeon RX 570 8 GB (Polaris, `gfx803`), Mesa RADV |
| Node | `192.168.0.28`, LAN `192.168.0.0/24` |
| Virtualization | none: `systemd-detect-virt` reports `none` |
| Containers | no Docker; buildah + podman, images imported into k3s |

## What runs

| Service | Address | Notes |
|---|---|---|
| Argo CD | `http://192.168.0.247/` | anonymous read-only, admin disabled |
| Grafana | `http://192.168.0.245/` | Cilium/Hubble dashboards, no login |
| VictoriaMetrics | `http://192.168.0.246/` | scrapes Cilium agent/operator/Hubble |
| Hubble UI | `http://192.168.0.241/` | flow visibility |
| Assistant API | `http://192.168.0.243/` | `POST /chat`, evidence panel, htmx UI |
| Neo4j Browser | `http://192.168.0.242/` | Cartography graph, `NEO4J_AUTH=none` |
| Cilium Ingress | `192.168.0.240` | shared ingress, class `cilium` |

Namespaces:

| Namespace | Running | Scaled to 0 |
|---|---|---|
| `assistant` | `assistant`, `assistant-e4-gpu` | `assistant-laya`, `assistant-e4-cpu` |
| `models` | `llm` (Qwen2.5-1.5B), `kokoro` | `laya` |
| `observability` | `grafana`, `victoriametrics` | — |
| `cartography` | `cartography-neo4j` + 6-hourly sync | — |
| `fuwa` | `fuwa`, `loki`, `otel-collector`, `tempo` | — |
| `worm-lab` | — | `worm-controller`, `worm-worker`, `vector`, `regression` |

`fuwa` and the E4 assistant variant were applied by hand; they are not Argo
Applications yet.

## Networking

Cilium 1.20.1 is the only CNI: kube-proxy replacement, shared ingress
controller, LB-IPAM plus L2 announcements for `192.168.0.240-250`. MetalLB is
not used; it belongs to the k3d profile.

Two settings in `config/k3s/config.yaml` are easy to break:

- `bind-address` stays unset. k3s advertises the node IP as the `kubernetes`
  endpoint, so binding the API to loopback leaves pods unable to reach the API
  and therefore DNS, while the node still reports `Ready`.
- `resolv-conf` points at `config/k3s/resolv.conf`. The host's `search
  mygateway` leaks into pods otherwise and stalls external lookups.

## GitOps

Argo CD 10.8.0 reconciles 12 Applications, all `Synced / Healthy`. Each
Application reads the branch its work lives on:

| targetRevision | Applications |
|---|---|
| `native-k3s-gpu-models` | assistant, assistant-laya, cartography, grafana, models-kokoro, models-laya, victoriametrics |
| `trackc` | models-llm, worm-controller, worm-regression, worm-vector, worm-worker |
| commit `592d7f51…` | `applications/stable/` bundle, manual sync only |

`main` holds the Phase-1 history and is not a deploy source. Moving the set
onto `cluster-evolution` is tracked in `ROADMAP.md` §4.

Argo CD runs anonymous read-only: `users.anonymous.enabled: "true"` with
`policy.default: role:readonly`, admin disabled, pinned at `192.168.0.247`.
`server.disable.auth` is never set, because it grants the Admin role to anyone.

## Models

| Model | Runtime | Footprint | Replicas |
|---|---|---|---|
| Qwen2.5-1.5B-Instruct | llama.cpp `llama-server`, Q4_K_M | ~1.5 GB RSS, 10-20 tok/s | 1 |
| Kokoro-82M TTS | kokoro-onnx | ~0.6 GB RSS, faster than realtime | 1 |
| Laya | `laya-serve` | ~2-3 GB, 193-464 ms/decision | 0 |

All three run on CPU. RAM is the constraint: platform is about 2-3 GiB and the
models about 7-8 GiB. A 3B model pushes past what fits with a browser open.

## GPU

| Node | Backs | On RX 570 (gfx803) |
|---|---|---|
| `/dev/dri/renderD128` | Vulkan, video (Mesa RADV / VA-API) | works, mounted into pods |
| `/dev/kfd` | ROCm compute | unsupported card, never mounted |

`apps/gpu-device-plugin/` advertises `amd.com/render` through the Device Plugin
gRPC API, so the render node is a schedulable resource with automatic mounting.
There is no per-process VRAM limit on this hardware: modelling the card as
slices (`amd.com/vram-gb`) makes admission advisory, not enforced.

E3 measured CPU vs GPU throughput for Qwen; see
`benchmark/experiments/e3-amd-gpu/RESULTS.md`.

The GPU is also the desktop's display card. It is bound to the host `amdgpu`
driver, and passing it to a VM would take it off the desktop until a reboot.
`AGENTS.md` records the rule: bare metal when the work needs `/dev/dri`, a VM
when it does not.

## Assistant

`apps/assistant/` exposes `POST /chat`. Each answer goes through two stages:

1. A deterministic bundle for common questions, or one PLAN call that picks
   read-only tools.
2. A FAN-OUT over those tools (Kubernetes API, PromQL, Neo4j, docs corpus),
   then one CORRELATE call that returns `{screen_md, speech}`.

Provenance (`at`, `source`, `reproduce`, `links`) is attached server-side after
the tools return, so the model cannot invent a link. A malformed reply is
retried once and then replaced by a digest of the evidence actually collected.
There are 11 tools, 10 of them read-only; `propose_action` mints a single-use
120-second token and needs a human confirmation plus `rbac.allowActions`.

Tests:

```bash
cd apps/assistant && python3 -m pytest -q
```

## Worm lab

The Phase-1 workload, still buildable, currently scaled to 0. A Rust worker
sends one idempotent replication intent to a Go controller that owns every
scale change and never exceeds `spec.maxReplicas`; Vector reads worker logs and
posts samples to an Elixir regression service. Workers hold no Kubernetes
credentials. A namespace `ResourceQuota` makes an over-cap request stay pending
instead of finding another way in.

## Running it

Native k3s, the live profile:

```bash
bash config/k3s/bootstrap.sh
kubectl apply -f config/argocd/applications/
```

k3d, the archived Phase-1 profile. This deletes and recreates the cluster named
`cilium-lab`:

```bash
bash start.sh
```

Tilt targets the `k3d-cilium-lab` context and does not create the cluster. Only
the user runs `tilt up` / `tilt down`, never an agent.

Without a GPU the platform runs in a VM (Vagrant or QEMU/KVM). Do not attach
the GPU to the guest.

## Tests and CI

```bash
bash config/ci/validate.sh      # Helm + kubeconform over config/ and apps/
```

`.github/workflows/validate.yaml` runs that script on pushes to `main` and on
pull requests. It does not validate the k3s profile, and the Cilium chart pin
predates `kubeProxyReplacement` (`bd experiments-wz1`).

## Limits

- One node, no HA, no TLS. Anonymous read-only is a LAN posture.
- Neo4j runs with `NEO4J_AUTH=none`; Grafana runs without login; the assistant
  API accepts CORS from any origin so the `fuwa` UI can call it
  (`bd experiments-yc4`).
- GPU work means Vulkan on an unsupported Polaris card. No ROCm, no CUDA, no
  VRAM enforcement.
- The models fit only with browsers closed. Swap is a cushion, not capacity.
- Exposure assumes a shared L2 segment for `192.168.0.240-250`. A routed cloud
  VPC needs a different path: `research/production-hosting-gke-gce.md`.
- Benchmarks are single-host runs tied to a recorded baseline commit.

## Layout

```text
ROADMAP.md               phases, provenance, open work
evolution/               per-phase write-up plus a commit index
research/                design notes, including the GKE/GCE hosting note
benchmark/               E2 planner A/B, E3 AMD GPU, E4 in progress
apps/
  assistant/             pipeline, tools, provenance, voice, memory
  assistant-laya/        isolated A/B variant, scaled to 0
  models/                llm/, kokoro/, laya/
  gpu-device-plugin/     Go plugin for amd.com/render
  controller/ worker/ regression/   Worm lab
config/
  k3s/                   live profile
  k3d/                   Phase-1 profile, archived
  argocd/                values and Applications
  cartography/ grafana/ victoriametrics/ vector/   platform services
  gpu/                   Vulkan smoke image and pods
  registry/ sealed-secrets/ metallb/ helm/ dns/ ci/
scripts/                 host helpers
Tiltfile*  start.sh      k3d entrypoints
docs/  cv/               gitignored, local only
```

## Where to read next

`ROADMAP.md` for the phases and open work, `evolution/` for the reasoning and
the commit that produced each layer, `research/` for design notes, `AGENTS.md`
for the rules agents have to follow.

This is a public repository. No credentials, kubeconfigs, certificates or
`.env` files belong in it.
