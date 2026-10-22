# Commit evidence index

Every phase claim resolves to a commit below. `git show <sha>` is the argument;
dates are author dates as recorded in the repository. This is a selection of
anchors, not the full history (204 commits across all branches at the time of
writing — `git log --all --oneline` for the rest).

Regenerate the per-phase counts:

```bash
git rev-list --count main                                        # 86
git rev-list --count native-k3s-gpu-models                       # 191
git rev-list --count trackc                                      # 115
git rev-list --count main..native-k3s-gpu-models                 # 105
git rev-list --left-right --count native-k3s-gpu-models...trackc  # 89  13
```

## Phase 1 — bounded Worm on k3d (`main`, tip `4ba7b97`)

| Date | Commit | Subject |
|---|---|---|
| 2026-09-05 | `1af4174` | bd init: initialize beads issue tracking |
| 2026-09-05 | `c0ecfc6` | feat(config): add k3s and Cilium bootstrap baseline |
| 2026-09-05 | `866f3b1` | feat(tilt): add root Tiltfile for config layout |
| 2026-09-05 | `bc644ad` | feat(experiments-w70): add private Argo CD GitOps bootstrap |
| 2026-09-05 | `d0d3a4e` | fix(experiments-u7p): enforce no-auth Argo CD |
| 2026-09-05 | `b973374` | feat(experiments-ef1): one-click full Tilt platform bootstrap |
| 2026-09-05 | `ed6d9d5` | feat(experiments-0fq): add Worm controller and CRD |
| 2026-09-05 | `b8ee85b` | feat(experiments-tz1): add Lua Wasm worker source |
| 2026-09-05 | `2eb5a08` | feat(experiments-319): add regression service |
| 2026-09-05 | `183dad1` | feat(experiments-k5s): configure Vector ETL chart |
| 2026-09-06 | `8c60385` | feat(experiments-8g7.2): replace worker with native Rust pod |
| 2026-09-06 | `bf0e523` | feat(experiments-8g7.3): add bounded Worm Helm charts |
| 2026-09-06 | `592d7f5` | feat(experiments-8g7.6): add explicit stable Argo ownership |
| 2026-09-07 | `bb93f8e` | fix(experiments-je1): use iptables masquerading for k3d |
| 2026-09-07 | `4ba7b97` | docs(experiments-8g7): describe bounded worm lab |

## Phase 2 — native k3s platform, Track A

| Date | Commit | Subject |
|---|---|---|
| 2026-10-06 | `9059891` | docs(experiments-3qw): record native k3s GPU/models plan and decisions |
| 2026-10-06 | `a2a7a41` | feat(experiments-ice): add native k3s platform profile |
| 2026-10-06 | `4368ef9` | fix(experiments-h62): listen on node IP so pods reach the k3s API |
| 2026-10-06 | `cee5c95` | feat(experiments-4kc): restore k3d exposure stack on native k3s |
| 2026-10-06 | `4042e67` | fix(experiments-4kc): correct LB IPAM block field and restart Cilium on config change |
| 2026-10-06 | `a5d75fb` | feat(experiments-mfh): use a cluster resolv-conf so pods resolve external names |
| 2026-10-06 | `dbcc855` | feat(experiments-9h8): add Cartography graph with Sealed Secrets |
| 2026-10-06 | `a8cd95d` | feat(experiments-7wz): add Cilium/Hubble observability (VictoriaMetrics + Grafana) |
| 2026-10-06 | `fc3e35b` | fix(experiments-d1c): reach k3s rootlessly via a local registry |
| 2026-10-06 | `aafcf31` | fix(experiments-r1h): make the k3s Cilium install idempotent |
| 2026-10-06 | `1ed3b42` | feat(experiments-c6f): add Argo CD Applications for models and GPU device plugin — **`trackc` fork point** |
| 2026-10-06 | `66df612` | fix(experiments-oit): widen Argo CD repo-server probe timeouts |
| 2026-10-08 | `954fbc9` | docs(experiments-6ks): Argo Applications are contextual (never main) |

## Phase 3 — CPU models, Track B

| Date | Commit | Subject |
|---|---|---|
| 2026-10-06 | `a958eed` | feat(experiments-mfh): add Laya CPU model app (Containerfile + chart) |
| 2026-10-06 | `e71aecd` | feat(experiments-mfh): add Kokoro TTS CPU model app (Containerfile + chart) |
| 2026-10-06 | `3baa2bd` | feat(experiments-mfh): add small LLM CPU model app (llama.cpp + Qwen2.5-1.5B) |
| 2026-10-06 | `a914679` | docs(experiments-mfh): add Track B models README |
| 2026-10-06 | `3655324` | fix(experiments-d1c): let the llm image run (root image vs runAsNonRoot) — on `trackc` |
| 2026-10-07 | `b92a5c7` | fix(experiments-ckx): let kokoro run (root image vs runAsNonRoot) |
| 2026-10-07 | `b9b4cc5` | feat(experiments-jhe): tighten worker memory request to 600Ki |
| 2026-10-08 | `9e977c8` | fix(experiments-jru): pin Laya to a non-root uid so the pod can start |
| 2026-10-08 | `b616cc2` | fix(experiments-jru): run Laya rootless (uid 1000) and point models-laya at native branch |
| 2026-10-08 | `160a211` | fix(experiments-jru): raise Laya memory limit to 6Gi (was OOMKilling) |
| 2026-10-08 | `87c4dd0` | fix(experiments-ub5): copy all assistant modules (provenance.py was omitted, broke the image) |
| 2026-10-10 | `6148491` | chore(experiments-187): scale Laya model to zero |

## Phase 4 — AMD GPU, Track C

| Date | Commit | Subject |
|---|---|---|
| 2026-10-06 | `5668f1a` | feat(experiments-ckq): add GPU device-access probe and Track C findings |
| 2026-10-06 | `4b6dbea` | feat(experiments-ckq): add Go GPU device plugin for amd.com/render |
| 2026-10-06 | `c252241` | feat(experiments-ckq): add Vulkan smoke-test image and pod (Track C step 3) — on `trackc` |
| 2026-10-06 | `0b8a527` | feat(experiments-ckq): verify Vulkan on RX 570; document device-cgroup requirement — on `trackc` |
| 2026-10-09 | `81c398d` | feat(experiments-qbf): E3 Vulkan llama.cpp build + spike manifest |
| 2026-10-09 | `5720602` | fix(experiments-qbf): add SPIRV-Headers to Vulkan build; make Kaniko wait configurable |
| 2026-10-18 | `b1073c2` | feat(experiments-qbf): E3 CPU-vs-GPU Qwen benchmark, results + teardown |
| 2026-10-18 | `30174f6` | docs(experiments-qbf): record E3 base commit hash — **tag `e3-amd-gpu`** |
| 2026-10-18 | `ef3c128` | docs(experiments-csn): E3 GPU single-pod contention probe — **`native-k3s-gpu-models` tip** |

## Phase 5 — AI operations assistant, Track D

| Date | Commit | Subject |
|---|---|---|
| 2026-10-06 | `aaf6f81` | feat(experiments-59i): add Milestone 1 assistant (text chat loop over the local LLM) — on `trackc` |
| 2026-10-06 | `8060efd` | feat(experiments-byj): assistant Milestone 2 - read-only get_pods tool, RBAC, audit — on `trackc` |
| 2026-10-06 | `ddb9519` | feat(experiments-754): assistant Milestone 3 - get_pod_logs and get_events tools — on `trackc` |
| 2026-10-06 | `74af8bf` | feat(experiments-2ji): add assistant M5 observability tools |
| 2026-10-06 | `ee52e12` | feat(experiments-2ji): assistant M5-M7 — observability, knowledge, investigation |
| 2026-10-07 | `2b9e126` | feat(experiments-ckx): assistant M8 — decoupled voice |
| 2026-10-07 | `9f12797` | feat(experiments-zn2): assistant M9 — controlled actions behind confirmation |
| 2026-10-07 | `3321626` | feat(experiments-dgi): assistant M10 — memory (notes + session history) |
| 2026-10-07 | `ba3629c` | feat(experiments-tn1): build the assistant in-cluster with Kaniko |
| 2026-10-07 | `db846fa` | feat(experiments-2ji): two-call pipeline (plan -> fan-out -> correlate) |
| 2026-10-07 | `6d867c7` | feat(experiments-2ji): per-stage LLM Prometheus instrumentation |
| 2026-10-08 | `70ee10b` | feat(experiments-bj5): evidence provenance + deep links (Grafana Explore, Hubble namespace) |
| 2026-10-08 | `0f921e1` | feat(experiments-6jc): E2 isolated Laya-vs-Qwen planner end-to-end benchmark |
| 2026-10-08 | `5586cc0` | docs(experiments-6jc): record E2 base commit hash — **tag `e2-laya-vs-qwen-e2e`** |
| 2026-10-09 | `8be6c07` | feat(experiments-mzm): get_topology (live k8s API) + get_topology_snapshot (Cartography/Neo4j) |
| 2026-10-09 | `4300050` | feat(experiments-7gg): in-process LLM concurrency limit + bounded FIFO queue |
| 2026-10-09 | `b945699` | fix(experiments-vbh): serve platform via bare LB IPs, drop home.arpa, enable Grafana iframe |
| 2026-10-12 | `b6a6581` | fix(experiments-1kz): persist Argo CD anonymous read-only at pinned LB; forbid anonymous admin |
| 2026-10-12 | `e415607` | feat(experiments-ve1): add CORS so an external UI can call the API |
| 2026-10-12 | `49a6436` | docs(experiments-3nt): document the CORS security exposure |

## Unification

| Date | Commit | Subject |
|---|---|---|
| 2026-10-22 | `9b449fe` | merge(experiments-5yi): fold the trackc worm-lab track into cluster-evolution |

The merge resolved 10 conflicting files in `native-k3s-gpu-models`' favour
(`README.md`, eight files under `apps/assistant/`, and
`config/argocd/applications/assistant.yaml`), because `trackc` last touched
those at its superseded Milestone 1–3 state.
