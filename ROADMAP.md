# Roadmap — how this repository evolved, and where it goes next

This repository did not start as a cluster platform. It started as a single
bounded-replication experiment on a throwaway `k3d` cluster and grew into a
native `k3s` cluster on a real Fedora host with a GPU, local models and an AI
operations assistant. This file records **why** the tree looks the way it does,
**which branch and commit** each era lives on, and **what is still open**.

Ground rules that shaped it (from [`AGENTS.md`](AGENTS.md)):

- Argo CD Applications never target `main`: it is the public face of the
  repository, not a deploy source. They read the working branch their work
  lives on.
- The top level stays small. Deployable configuration is `config/`, executable
  software is `apps/`, and tracked narrative material is `research/` +
  `evolution/`. `docs/` is gitignored on purpose (machine-specific runbooks).
- Every commit is atomic and cites its `bd` issue.

---

## 0. TL;DR — state on 2026-10-28

| | |
|---|---|
| Execution model | **Bare metal on the physical Fedora 44 host** — not a VM, Vagrant, QEMU/libvirt, or container (`systemd-detect-virt` → `none`). The GPU is the host's display card; a passthrough bind would remove it from the host. |
| Running profile | **Native k3s** on one Fedora 44 host (node `192.168.0.28`), not `k3d` |
| Networking | **Cilium only** — eBPF, kube-proxy replacement, Cilium Ingress, Cilium LB-IPAM + L2 announcements on the LAN (`192.168.0.240-250`). MetalLB is **k3d-era only**. |
| GitOps | Argo CD `10.8.0`, 17 Applications registered, **anonymous read-only**, pinned at `192.168.0.247`. Four of them are Kargo's and are `Synced/Healthy`; measured 2026-10-28: `cartography` `OutOfSync/Degraded`, `grafana` and `victoriametrics` `OutOfSync` |
| Promotion | Kargo `1.12.0` in namespace `kargo`, UI at `http://192.168.0.249/` behind a token-injecting gateway. The busybox pilot promoted staging → prod as two real commits (`5c5d204`, `ced5f89`) |
| Models | Qwen2.5-1.5B (llama.cpp, CPU) and Kokoro TTS running; Laya scaled to 0 |
| GPU | AMD RX 570 (`gfx803`) reachable as `amd.com/render` through a Go device plugin; Vulkan path proven, **no ROCm/CUDA, no VRAM enforcement** |
| AI Ops | `apps/assistant` — 15 tools, one evaluator call reads the sentence and returns the task list, evidence attached server-side |
| Worm lab | Complete on `trackc`, merged here, and **scaled to 0** — it is preserved history, not a live workload |
| Branches | `main` (public face, not a deploy source) · `native-k3s-gpu-models` (live Argo target) · `trackc` (worm-lab + registry backports) · `cluster-evolution` (unifies all three) · `feat/kargo` (Phase 6: Kargo + assistant evaluator) |

---

## 1. Branch topology

```text
1af4174  bd init                                           2026-09-05
   │
   │  Phase 1 — bounded Worm on k3d  (86 commits, blog/ + Tilt + MetalLB)
   ▼
4ba7b97  docs(experiments-8g7): describe bounded worm lab  2026-09-07
   │
   │  Phase 2-4 — native k3s, CPU models, GPU, AI Ops  (101 commits after main)
   ▼
ab05883  docs(experiments-csn): E3 GPU single-pod probe    2026-10-18  ← native-k3s-gpu-models tip
   │
   ├───────────────┐  at 1ed3b42 (2026-10-06) the worm-lab work forked off to trackc
   │               ▼
   │        89f20f9  chore(experiments-qbf): scale worm-lab workloads to 0  ← trackc tip
   │               │
   ▼               ▼
e6a4852  merge(experiments-5yi): fold the trackc worm-lab track into cluster-evolution
   │
   ▼
2404f70  Merge pull request #1 from Schywi/cluster-evolution   2026-10-25
         ├── main (identical tree; the public face)
         └── cluster-evolution
   │
   │  Phase 6 — Kargo promotion + assistant evaluator  (40 commits)
   ▼
45d5001  fix(experiments-sl1): observe_url falls back to the evaluator's query
         └── feat/kargo
```

Measured divergence (after the cv purge, which removed 4 commits from
`cluster-evolution`'s history; see `evolution/evidence.md`):

| Pair | Merge base | Left-only | Right-only |
|---|---|---|---|
| `main` ↔ `native-k3s-gpu-models` | `4ba7b97` | 0 | 101 |
| `trackc` ↔ `native-k3s-gpu-models` | `1ed3b42` | 13 | 85 |
| `cluster-evolution` ↔ `feat/kargo` | `2404f70` | 0 | 40 |
| `main` ↔ `cluster-evolution` | `2404f70` | 0 | 0 |

`trackc`'s 13 commits are worm-lab/backport work; its assistant files were the
superseded Milestone 1–3 prototype, which is why the merge resolved all 10
conflicting files in `native-k3s-gpu-models`' favour (see commit `e6a4852`).

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
`81c398d`/`5720602` (Vulkan llama.cpp build), tag `e3-amd-gpu` at `9b5554e`,
`a07bced` + `ab05883` (E3 CPU-vs-GPU benchmark and contention probe).
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

### Phase 6 — Promotion with Kargo, and a planner that reads the whole sentence (2026-10-24 → 10-28)

**Branch:** `feat/kargo`, forked from `cluster-evolution` at `2404f70`; 40
commits, tip `45d5001`. **Tracked docs:** `config/kargo/README.md`,
`research/features/assistant/`,
[`evolution/06-kargo-promotion-pipeline.md`](evolution/06-kargo-promotion-pipeline.md).

Kargo `1.12.0` turns a promotion into a commit: the pilot Warehouse watches
`docker.io/library/busybox`, staging auto-promotes, production needs a manual
`Promotion`, and both stages end as real commits in this repository pointed at by
Argo CD (`5c5d204`, `ced5f89`). The install runs without cert-manager
(`gen-certs.sh` signs the API and webhooks certificates with a local CA) and
without anonymous access (Kargo has none; `kargo-gateway` logs in and injects
the token, at `192.168.0.249`).

The same branch replaces the assistant's keyword router with one evaluator call
that reads the sentence and returns the task list (`4ca2dd5`), adds recipes for
ordered chains that hand a URL between steps (`e58ee40`), and adds a sandbox
specialist that is a thin client over the console API (`5508da9`). Strict tool
schemas reject a malformed call before it runs (`436563f`).

What breaks is in the phase document: `.248` was allocated to a
`default/kargo-gateway` Service and did not come back when it was deleted, and
anything that reaches `.249` holds Kargo admin, including the credential that
pushes to this repository.

---

## 3. What is actually deployed (verified against the cluster)

| App | Namespace | Argo targetRevision | Exposure |
|---|---|---|---|
| `assistant` | `assistant` | `native-k3s-gpu-models` | `192.168.0.243` |
| `assistant-laya` | `assistant` | `native-k3s-gpu-models` | scaled to 0 |
| `cartography` | `cartography` | `native-k3s-gpu-models` | `192.168.0.242` (Neo4j, no auth) |
| `fuwa` | `fuwa` | `k3s-integration` of `github.com/Schywi/fuwa.git` | `192.168.0.244` |
| `grafana` | `observability` | `native-k3s-gpu-models` | `192.168.0.245` |
| `kargo-gateway` | `kargo` | `feat/kargo` | `192.168.0.249` |
| `kargo-pilot-resources` | `pilot` | `feat/kargo` | internal |
| `kargo-pilot-staging` | `kargo-pilot-staging` | commit `5c5d204` | internal |
| `kargo-pilot-prod` | `kargo-pilot-prod` | commit `ced5f89` | internal |
| `victoriametrics` | `observability` | `native-k3s-gpu-models` | `192.168.0.246` |
| `models-llm` | `models` | `trackc` | internal |
| `models-kokoro` | `models` | `native-k3s-gpu-models` | internal |
| `models-laya` | `models` | `native-k3s-gpu-models` | scaled to 0 |
| `worm-vector` | `worm-lab` | `trackc` | scaled to 0 |
| `worm-controller` | `worm-lab` | `trackc` | scaled to 0 |
| `worm-worker` | `worm-lab` | `trackc` | scaled to 0 |
| `worm-regression` | `worm-lab` | `trackc` | scaled to 0 |

Measured 2026-10-28 with `kubectl -n argocd get app -o
custom-columns=...`. Three Applications are not `Synced/Healthy`: `cartography`
is `OutOfSync/Degraded`, `grafana` and `victoriametrics` are `OutOfSync`. The two
Kargo environment Applications are pinned by `argocd-update` at the promotion
commits, so they do not follow a branch.

Shared ingress and UI: `cilium-ingress` `192.168.0.240`, Hubble UI
`192.168.0.241`, Kargo `192.168.0.249`, Argo CD `192.168.0.247`.

One thing deploys **outside** Argo and is therefore not reproducible from a
clean checkout: the E4 A/B assistant (`assistant-e4-gpu`, tracked as
`config/gpu-exp/assistant-e4-gpu.yaml`). Registering it as an Application is
tracked in §4.

`config/argocd/applications/stable/` holds four Applications pinned to a full
commit (`592d7f51…`) with manual sync only. Per
`config/argocd/README.md` they must not be registered together with
`applications/vector.yaml` — both describe the same Vector resources.

---

## 4. Open work

Grouped from `bd ready`; run `bd ready` for the live list.

**Promotion (do first after this branch lands)**
1. Point the 12 live Applications at the unified branch — today 7 read
   `native-k3s-gpu-models`, 5 read `trackc`, and `fuwa` reads its own repository
   (`k3s-integration` of `Schwyi/fuwa.git`). Flipping `targetRevision` is what
   makes "one branch, one cluster" real; it is deliberately **not** done in this
   commit because it changes what the running cluster reconciles from.
2. Re-point `kargo-gateway` and `kargo-pilot-resources` from `feat/kargo` to
   `cluster-evolution` before this branch is retired. The two environment
   Applications do not need it: `argocd-update` pinned them to the promotion
   commits `5c5d204` and `ced5f89`.
3. Decide the fate of `main` and `trackc` once `cluster-evolution` is the
   working branch (keep as archives, or tag and freeze).

**Legacy drift**
4. `experiments-wz1` — `config/ci/validate.sh` pins an old Cilium chart and
   fails on `kubeProxyReplacement`; CI does not validate the k3s profile.
5. `experiments-c6f` — wire the model and GPU charts into CI and Argo CD
   (`gpu-device-plugin.yaml` exists but is not registered in the cluster).
6. `AGENTS.md` refreshed for the native profile (`experiments-47n`): bare-metal
   execution model with the GPU prohibition, real repository layout, current
   `targetRevision` examples, testing commands. Remaining drift there is only
   the generated Beads/Codex blocks, which are owned by `bd setup codex`.
7. `README.md`-era leftover: `start.sh`, `Tiltfile` and `scripts/deploy-worm.sh`
   only work against `k3d-cilium-lab`. They are Phase-1 artifacts; either gate
   them behind an explicit "k3d profile" banner or retire them.
8. `cartography` is `OutOfSync/Degraded`, `grafana` and `victoriametrics` are
   `OutOfSync` (measured 2026-10-28). An `OutOfSync` Application is not a
   promotion candidate; find out why before the next `targetRevision` flip.

**Platform**
9. `experiments-ice` / `mfh` / `ckq` — Track A/B/C follow-ups still open in bd.
10. Register the E4 A/B assistant (`assistant-e4-gpu`) as an Application, or
    document it as intentionally manual.
11. VPS/public showcase profile (`config/vps/` + Cloudflare Tunnel) remains a
    plan, not code. The Fedora LAN profile stays private.
12. Cloud hosting on GKE/GCE: Cilium LB IPAM and L2 announcements **cannot work
    there** (routed VPC; Cilium is managed on GKE). The full inventory, exposure
    matrix and migration checklist live in
    [`research/production-hosting-gke-gce.md`](research/production-hosting-gke-gce.md);
    no code exists yet (`bd experiments-9q0`).

**Assistant**
13. `experiments-yc4` (permissive CORS on an unauthenticated API),
    `experiments-vf4` (chat tool call off the event loop), `experiments-oon`
    (drop planned tools missing required args), `experiments-qib` (pin the
    output contract with pytest).
14. `apps/assistant` full-suite ordering: `pytest -q` fails
    `test_recipes.py::test_observe_cilium_uses_the_namespace_url` because
    `test_guardrails.py` reloads the `provenance` module with
    `HUBBLE_URL=http://h` and leaves it reloaded. The file passes alone (8
    passed); the suite does not.

---

## 5. Known limits — stated, not hidden

- **Not production.** One host, one node, no HA, no TLS, no auth on Neo4j,
  Grafana runs deliberately without login. Anonymous read-only is a lab
  posture, not a security model for a public IP.
- **GPU is Vulkan-only.** No ROCm, no CUDA, no VRAM enforcement. Any claim of
  "GPU compute" on this repo means *graphics/Vulkan inference on an unsupported
  Polaris card*.
- **The GPU is also the desktop's display card.** It is shared with the host
  session, so there is no headless-only GPU here and no VM boundary to hide
  behind: a passthrough bind would take the card off the desktop entirely.
- **Memory-bound.** The models fit only with the browser closed; swap is
  enabled as a cushion (`memorySwap.swapBehavior`), not as capacity.
- **Two reconcilers are never allowed on one resource.** That is why the pinned
  `stable/` bundle and Tilt-driven Worm resources are mutually exclusive.
- **Benchmarks are single-host measurements.** E2/E3 record a baseline commit
  hash for a reason: the numbers are only valid for that revision.
- **The exposure design is LAN-only.** It relies on a real L2 segment for
  `192.168.0.240-250`; a routed cloud VPC (GKE/GCE) cannot announce those
  addresses, so hosting there means reconfiguring exposure first — see
  [`research/production-hosting-gke-gce.md`](research/production-hosting-gke-gce.md).

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
  kargo/              Phase 6 promotion: install, gateway, pilot Warehouse/Stages
  gpu-exp/            E4/A-B GPU manifests (tracked, not yet Applications)
  argocd/             values and Applications (incl. the four kargo-* ones)
scripts/              host-side helpers
Tiltfile*             Phase 1 entrypoints (k3d only)
docs/                 gitignored, machine-specific runbooks
```

If the platform ever outgrows this shape, the next step is
`config/profiles/{k3d,k3s,vps}/` with the Argo Application paths updated in the
same commit — one coherent rename, not a slow drift.
