# Roadmap — how this repository evolved, and where it goes next

This repository did not start as a cluster platform. It started as a single
bounded-replication experiment on a throwaway `k3d` cluster and grew into a
native `k3s` cluster on a real Fedora host with a GPU, local models and an AI
operations assistant. This file records **why** the tree looks the way it does,
**which branch and commit** each era lives on, and **what is still open**.

Ground rules that shaped it (from [`AGENTS.md`](AGENTS.md)):

- Argo CD Applications never target `main`; they read the branch their work
  lives on.
- The top level stays small. Deployable configuration is `config/`, executable
  software is `apps/`, and tracked narrative material is `research/` +
  `evolution/`. `docs/` is gitignored on purpose (machine-specific runbooks).
- Every commit is atomic and cites its `bd` issue.

---

## 0. TL;DR — state on 2026-10-22

| | |
|---|---|
| Running profile | **Native k3s** on one Fedora 44 host (node `192.168.0.28`), not `k3d` |
| Networking | **Cilium only** — eBPF, kube-proxy replacement, Cilium Ingress, Cilium LB-IPAM + L2 announcements on the LAN (`192.168.0.240-250`). MetalLB is **k3d-era only**. |
| GitOps | Argo CD `10.8.0`, 12 Applications `Synced/Healthy`, **anonymous read-only**, pinned at `192.168.0.247` |
| Models | Qwen2.5-1.5B (llama.cpp, CPU) and Kokoro TTS running; Laya scaled to 0 |
| GPU | AMD RX 570 (`gfx803`) reachable as `amd.com/render` through a Go device plugin; Vulkan path proven, **no ROCm/CUDA, no VRAM enforcement** |
| AI Ops | `apps/assistant` — 10 read-only tools, 2 LLM calls per answer, evidence attached server-side |
| Worm lab | Complete on `trackc`, merged here, and **scaled to 0** — it is preserved history, not a live workload |
| Branches | `main` (frozen k3d era) · `native-k3s-gpu-models` (live Argo target) · `trackc` (worm-lab + registry backports) · **`cluster-evolution` (this branch, unifies all three)** |

---

## 1. Branch topology

```text
1af4174  bd init                                           2026-09-05
   │
   │  Phase 1 — bounded Worm on k3d  (86 commits, blog/ + Tilt + MetalLB)
   ▼
4ba7b97  docs(experiments-8g7): describe bounded worm lab  2026-09-07  ← main frozen here
   │
   │  Phase 2-4 — native k3s, CPU models, GPU, AI Ops  (191 commits)
   ▼
ef3c128  docs(experiments-csn): E3 GPU single-pod probe    2026-10-18  ← native-k3s-gpu-models tip
   │
   ├───────────────┐  at 1ed3b42 (2026-10-06) the worm-lab work forked off to trackc
   │               ▼
   │        89f20f9  chore(experiments-qbf): scale worm-lab workloads to 0  ← trackc tip
   │               │
   ▼               ▼
9b449fe  merge(experiments-5yi): fold the trackc worm-lab track into cluster-evolution
         └── cluster-evolution (this branch)
```

Measured divergence before unification:

| Pair | Merge base | Left-only | Right-only |
|---|---|---|---|
| `main` ↔ `native-k3s-gpu-models` | `4ba7b97` | 0 | 105 |
| `trackc` ↔ `native-k3s-gpu-models` | `1ed3b42` | 13 | 89 |

`trackc`'s 13 commits are worm-lab/backport work; its assistant files were the
superseded Milestone 1–3 prototype, which is why the merge resolved all 10
conflicting files in `native-k3s-gpu-models`' favour (see commit `9b449fe`).

---

## 2. Phases

### Phase 1 — Bounded Worm replication on `k3d` (2026-09-05 → 09-07)

**Branch:** `main`, 86 commits, tip `4ba7b97`. **Tracked docs:** `blog/`.

The question: *can self-replication be bounded, idempotent and observable?* A
Go controller reconciles a `Worm` CRD and owns worker scale; a native Rust
worker emits one retry-safe replication intent plus JSON samples; Vector reads
node logs and forwards to an Elixir regression service; Cilium/Hubble show the
flows. A Lua/Wasm worker variant and a `wasmtime`/runwasi shim were explored on
the same cluster (`config/k3d/wasmtime/`).

Shipped and still in the tree: `apps/{controller,worker,regression}` + their
charts, `config/k3d/`, `config/vector/`, `Tiltfile*`, `start.sh`,
`scripts/deploy-worm.sh`, `blog/2026-09-0*.md`.

**What it proved:** bounded self-replication with `spec.maxReplicas` and a
namespace `ResourceQuota` — a desired count above the quota *stays pending*
instead of bypassing the cap.

**Why it ended:** two hard blockers, both recorded in
[`research/native-k3s-gpu-models-plan.md`](research/native-k3s-gpu-models-plan.md):
the k3d container kernel rejected Cilium 1.20's BPF SNAT program (forced
iptables masquerading + host-legacy routing), and the base environment had no
systemd/eBPF. Also: a GPU inside a Docker-in-Docker node is not a place to learn
device plugins.

### Phase 2 — Native `k3s` on the Fedora host, Track A (2026-10-06)

**Branch:** `native-k3s-gpu-models`, anchors `a2a7a41` → `cee5c95` → `4368ef9`.
**Tracked docs:** `config/k3s/README.md`, `research/native-k3s-gpu-models-plan.md`.

Same components, real kernel: `/etc/rancher/k3s/config.yaml` disables Flannel,
kube-proxy, Traefik and ServiceLB (`config/k3s/config.yaml`), Cilium 1.20.1 owns
everything, and MetalLB is replaced by **Cilium LB-IPAM + L2 announcements** on
the LAN (`config/k3s/cilium-lb-ipam.yaml`). Rootless image delivery moved from
`k3d image import` to an in-cluster registry plus `k3s ctr`/Kaniko
(`config/registry/`, `config/k3s/import-image.sh`).

Two decisions worth remembering, both recorded as comments in
`config/k3s/config.yaml`:

- **`bind-address` must stay unset.** k3s advertises the *node IP* as the
  `kubernetes` endpoint, so a loopback-only API leaves every pod unable to reach
  the API — and therefore DNS — while the node still reports `Ready`.
- **A cluster-owned `resolv-conf`** replaces the host's: the host carries
  `search mygateway`, which leaks into pods and stalls external name resolution.

### Phase 3 — CPU models inside the cluster, Track B (2026-10-06)

**Anchors:** `a958eed` (Laya), `e71aecd` (Kokoro), `3baa2bd` (1.5B LLM).
**Paths:** `apps/models/{laya,kokoro,llm}`.

Each model is a Deployment + Service built as a Containerfile, called over
ClusterIP/port-forward, observable in Hubble. Policy recorded in the plan doc:
**every model starts on CPU**; GPU is a separate, later track. RAM — not CPU or
GPU — is the binding constraint on this host (~9 GiB free, ~7-8 GiB of models).

### Phase 4 — AMD GPU, Track C (2026-10-06 → 2026-10-18)

**Anchors:** `5668f1a` (device probe), `4b6dbea` (Go device plugin),
`c252241`/`0b8a527` on `trackc` (Vulkan smoke test on the RX 570),
`81c398d`/`5720602` (Vulkan llama.cpp build), tag `e3-amd-gpu` at `30174f6`,
`b1073c2` + `ef3c128` (E3 CPU-vs-GPU benchmark and contention probe).
**Paths:** `apps/gpu-device-plugin/`, `config/gpu/`, `benchmark/experiments/e3-amd-gpu/`.

The honest result, and the most useful one to keep:

| Node | Backs | On RX 570 (gfx803) |
|---|---|---|
| `/dev/dri/renderD128` | graphics, **Vulkan**, video | ✅ works, mounted into pods |
| `/dev/kfd` | AMD **compute** (ROCm) | ❌ unsupported card — never mounted |

The Go device plugin advertises `amd.com/render` so the GPU is a **schedulable,
counted resource** with automatic mounting. It does **not** give per-pod VRAM
quotas: a slice model (`amd.com/vram-gb`) can only make admission *advisory*,
because raw `amdgpu`/Vulkan on gfx803 exposes no per-process memory limit. E3
measured what the card can actually do instead of assuming ROCm would work.

### Phase 5 — AI operations assistant, Track D (2026-10-06 → 2026-10-12)

**Anchors:** M1 `aaf6f81`, M2 `8060efd`, M3 `ddb9519` (on `trackc`) → M5 `74af8bf`,
M5-M7 `ee52e12`, M8 `2b9e126`, M9 `9f12797`, M10 `3321626`; pipeline `db846fa`;
provenance `70ee10b`; topology `8be6c07`; bounded queue `4300050`.
**Benchmarks:** tag `e2-laya-vs-qwen-e2e` at `5586cc0` (E2), `benchmark/`.
**Paths:** `apps/assistant/`, `apps/assistant-laya/`, `research/features/`.

The assistant answers infrastructure questions *with evidence*: a deterministic
bundle covers 17 of 27 benchmark intents at zero inference cost, everything else
takes one PLAN call and one CORRELATE call; provenance (`at`, `source`,
`reproduce`, `links`) is attached server-side so the model cannot fabricate a
link; malformed output fails closed to a digest of the facts actually gathered.
Read-only by construction (`get/list/watch` on a fixed resource set); the only
mutating tool mints a single-use 120-second confirmation token.

Phase 5 also produced the exposure posture that the cluster runs on today:
bare Cilium LB IPs instead of DNS/hosts tricks (`b945699`), Grafana and the
Neo4j Browser reachable read-only, and **Argo CD anonymous read-only pinned to
`192.168.0.247`** with `server.disable.auth` forbidden (`b6a6581`). The
permissive CORS change (`e415607`) is documented as a known exposure in
`research/`/`bd experiments-yc4`, not as an oversight.

---

## 3. What is actually deployed (verified against the cluster)

| App | Namespace | Argo targetRevision | Exposure |
|---|---|---|---|
| `assistant` | `assistant` | `native-k3s-gpu-models` | `192.168.0.243` |
| `assistant-laya` | `assistant` | `native-k3s-gpu-models` | scaled to 0 |
| `cartography` | `cartography` | `native-k3s-gpu-models` | `192.168.0.242` (Neo4j, no auth) |
| `grafana` | `observability` | `native-k3s-gpu-models` | `192.168.0.245` |
| `victoriametrics` | `observability` | `native-k3s-gpu-models` | `192.168.0.246` |
| `models-llm` | `models` | `trackc` | internal |
| `models-kokoro` | `models` | `native-k3s-gpu-models` | internal |
| `models-laya` | `models` | `native-k3s-gpu-models` | scaled to 0 |
| `worm-vector` | `worm-lab` | `trackc` | scaled to 0 |
| `worm-controller` | `worm-lab` | `trackc` | scaled to 0 |
| `worm-worker` | `worm-lab` | `trackc` | scaled to 0 |
| `worm-regression` | `worm-lab` | `trackc` | scaled to 0 |

Shared ingress and UI: `cilium-ingress` `192.168.0.240`, Hubble UI
`192.168.0.241`, Argo CD `192.168.0.247`.

Two things deploy **outside** Argo and are therefore not reproducible from a
clean checkout: the `fuwa` namespace (UI + loki/otel/tempo at `192.168.0.244`)
and the E4 A/B assistant (`assistant-e4-gpu`). Registering them as Applications
is tracked in §4.

`config/argocd/applications/stable/` holds four Applications pinned to a full
commit (`592d7f51…`) with manual sync only. Per
`config/argocd/README.md` they must not be registered together with
`applications/vector.yaml` — both describe the same Vector resources.

---

## 4. Open work

Grouped from `bd ready`; run `bd ready` for the live list.

**Promotion (do first after this branch lands)**
1. Point the 12 live Applications at the unified branch — today 7 read
   `native-k3s-gpu-models`, 6 read `trackc`. Flipping `targetRevision` is what
   makes "one branch, one cluster" real; it is deliberately **not** done in this
   commit because it changes what the running cluster reconciles from.
2. Decide the fate of `main` and `trackc` once `cluster-evolution` is the
   working branch (keep as archives, or tag and freeze).

**Legacy drift**
3. `experiments-wz1` — `config/ci/validate.sh` pins an old Cilium chart and
   fails on `kubeProxyReplacement`; CI does not validate the k3s profile.
4. `experiments-c6f` — wire the model and GPU charts into CI and Argo CD
   (`gpu-device-plugin.yaml` exists but is not registered in the cluster).
5. `AGENTS.md` still describes the k3d era ("headless/no-auth local GitOps",
   `ClusterIP` only) and labels the Argo CD rule as absolute while the cluster
   now pins a `LoadBalancer` IP. Reconcile it before relying on it.
6. `README.md`-era leftover: `start.sh`, `Tiltfile` and `scripts/deploy-worm.sh`
   only work against `k3d-cilium-lab`. They are Phase-1 artifacts; either gate
   them behind an explicit "k3d profile" banner or retire them.

**Platform**
7. `experiments-ice` / `mfh` / `ckq` — Track A/B/C follow-ups still open in bd.
8. Register `fuwa` and the E4 A/B assistant as Applications (or document them
   as intentionally manual).
9. VPS/public showcase profile (`config/vps/` + Cloudflare Tunnel) remains a
   plan, not code. The Fedora LAN profile stays private.

**Assistant**
10. `experiments-yc4` (permissive CORS on an unauthenticated API),
    `experiments-vf4` (chat tool call off the event loop), `experiments-oon`
    (drop planned tools missing required args), `experiments-qib` (pin the
    output contract with pytest).

---

## 5. Known limits — stated, not hidden

- **Not production.** One host, one node, no HA, no TLS, no auth on Neo4j,
  Grafana runs deliberately without login. Anonymous read-only is a lab
  posture, not a security model for a public IP.
- **GPU is Vulkan-only.** No ROCm, no CUDA, no VRAM enforcement. Any claim of
  "GPU compute" on this repo means *graphics/Vulkan inference on an unsupported
  Polaris card*.
- **Memory-bound.** The models fit only with the browser closed; swap is
  enabled as a cushion (`memorySwap.swapBehavior`), not as capacity.
- **Two reconcilers are never allowed on one resource.** That is why the pinned
  `stable/` bundle and Tilt-driven Worm resources are mutually exclusive.
- **Benchmarks are single-host measurements.** E2/E3 record a baseline commit
  hash for a reason: the numbers are only valid for that revision.

---

## 6. Layout: what belongs where

The structure is **additive by decision**: no path under `config/`, `apps/` or
`scripts/` moved, because every Argo CD Application hardcodes its `path` and the
Tiltfiles assume the same layout. The evolution is documented, not relocated.

```text
ROADMAP.md            this file — phases, provenance, open work
evolution/            per-phase narrative with commit/tag evidence
research/             tracked design notes and shipped feature docs
blog/                 Phase-1 write-ups (k3d / Worm / Tilt)
benchmark/            measured experiments (E2 planner A/B, E3 AMD GPU, E4 …)
apps/                 executable software (assistant, models, worm lab, GPU plugin)
config/               deployable configuration
  k3d/                Phase 1 profile (archived, still runnable on Docker)
  k3s/                Phase 2+ profile (the live one)
scripts/              host-side helpers
Tiltfile*             Phase 1 entrypoints (k3d only)
docs/                 gitignored, machine-specific runbooks
```

If the platform ever outgrows this shape, the next step is
`config/profiles/{k3d,k3s,vps}/` with the Argo Application paths updated in the
same commit — one coherent rename, not a slow drift.
