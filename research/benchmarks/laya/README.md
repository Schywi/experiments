---
title: 'Laya on Vulkan — can the RX 570 beat the CPU Laya server on decision latency?'
date: 2026-11-09
kind: experiment
state: research
tags: [state-research, laya, gpu, vulkan, benchmark]
---

# Laya on Vulkan — can the RX 570 beat the CPU Laya server on decision latency?

| | |
|---|---|
| Opened | 2026-11-09 |
| Status | Built, not yet measured. No latency numbers exist for the GPU arm. |
| Issue | `experiments-wti` |
| Files | this directory only |
| Baseline | CPU Laya, `apps/models/laya`: 400–500 ms per decision on this host |
| Branch | `docs-site` |

Laya is a non-autoregressive decision engine: one request is one forward pass and
the answer is a choice, a score or a yes/no. Nothing is generated, so tokens per
second describes nothing here. The measurement is wall time of one
`POST /v1/systemone`.

On this host the Python package (`laya[serve]`) is CPU-only. Its accelerator path
is TileLang/CUDA, and this card has no CUDA. The only GPU route Laya has here is
`laya.cpp`, ggml-based, with a Vulkan backend that runs on Mesa RADV.

## Question

Does serving Laya through `laya.cpp --vulkan` on the RX 570 reduce per-decision
latency below the 400–500 ms the CPU server takes?

Pre-registered success criterion, fixed before the run so the result cannot be
reinterpreted afterwards: **GPU p50 ≤ 0.7 × CPU p50, or sustained throughput
≥ 2 × CPU.** Between 0.7× and 1.3× the finding is "no speedup worth the
maintenance", and that gets published as the answer.

## Fixed configuration

| Item | Value |
|---|---|
| Backend | `laya.cpp` Vulkan (`ggml`), Mesa RADV |
| Upstream | `github.com/lkarlslund/laya.cpp` at `e632da0428236be56978e53ddc92cdcd565fb493` |
| Checkpoint | `convaiinnovations/laya` revision `1c5edc17a7acd8701df6fc341c0d179f1c62c982` |
| Variant | `english` (421M) — see VRAM below |
| Precision | plain FP32 |
| Batching | off (`--no-batching`) |
| Corpus | `prompts.jsonl`, 12 cases, sha256 `803a1dbfe1e51bd8bb704ab5809161100ee9cceb6a8e62ecfd415483e75d2ed4` |
| Repeats | 1 warmup + 5 timed per prompt |
| GPU grant | `amd.com/render: 1` (device plugin), not a `/dev/dri` hostPath |

`english` only. The card reports 4 GiB VRAM
(`/sys/class/drm/card1/device/mem_info_vram_total` = 4294967296) and the desktop
compositor draws from the same memory. A 421M checkpoint in FP32 is ~1.7 GB, so
one variant fits and three do not. `multilingual` and `typed-decisions` need
their own run with the variant changed in the manifest and the init container.

Plain FP32 because gfx803 has no FP16 arithmetic and no
`VK_KHR_cooperative_matrix`, which `--tensor-core-fp32` requires.

## Compensation the number needs

Two properties of this card make a GPU win less likely than the E3 result
(llama.cpp, 6× generation speedup) suggests:

- 421M parameters in one forward pass with ~400 ms of work is small for a
  compute-offload win. Under a fixed per-dispatch cost, Vulkan on Polaris
  frequently loses to six CPU cores on graphs this size.
- FP32 on a card with weak FP16 means the GPU sees no throughput advantage from
  reduced precision; the CPU does not either, so the comparison is fair, just
  not favourable.

`laya.cpp` has no `--threads` flag, so the CPU reference runs on all cores.
That is the CPU's best case and biases the comparison against the GPU arm on
purpose.

## Run book

Build on the host (rootless buildah; the image is not in the registry yet):

```bash
buildah bud -t laya-cpp-vulkan:local \
  -f research/benchmarks/laya/Containerfile research/benchmarks/laya
buildah push laya-cpp-vulkan:local oci-archive:/tmp/laya-cpp-vulkan.tar
sudo k3s ctr images import /tmp/laya-cpp-vulkan.tar
```

Gate before applying — the grant must exist:

```bash
kubectl get node -o jsonpath='{.items[*].status.allocatable.amd\.com/render}'
```

`1` means the device plugin is advertising the render node. `0` means kubelet and
the plugin are out of sync; on 2026-11-09 it printed `0` while the plugin logged
`advertising 1 render node(s) [renderD128]`, so this gate is real and a pod
applied through it stays `Pending` on the extended resource. Restarting the
plugin's DaemonSet pod re-registers it. That repair is hand-made outside this
experiment and is not automated here.

Apply, wait for the listener, measure:

```bash
kubectl apply -f research/benchmarks/laya/manifests/laya-vulkan.yaml
kubectl -n gpu-exp rollout status deploy/laya-vulkan --timeout=10m
kubectl -n gpu-exp port-forward svc/laya-vulkan 8080:8080 &
python3 research/benchmarks/laya/measure.py \
  --url http://127.0.0.1:8080 --label vulkan-english \
  --repeats 5 --reference-ms 450 --out research/benchmarks/laya/results/vulkan-english.json
```

Teardown:

```bash
kubectl delete -f research/benchmarks/laya/manifests/laya-vulkan.yaml
```

The objects are hand-applied experiment objects in namespace `gpu-exp`, the same
lifecycle `benchmark/experiments/e3-amd-gpu` and `e4-gpu-e2e` use. Nothing here
is GitOps-managed, so the platform cannot drift onto it.

## What the runner records

`measure.py` posts each corpus case `5` times after one discarded warmup, and
reports mean/p50/p95/min/max per prompt and overall, plus decisions per second.
The first call of the run is reported separately as `cold_start_ms`: Vulkan
compiles SPIR-V pipelines per graph shape on first dispatch, which costs seconds
once and nothing afterwards. Folding it into the steady-state number would
overstate or understate the cost depending on how many repeats were used.

Every result file carries the label, URL, corpus path and the raw per-call
samples, so a table in this note can always be traced back to a run.

## Results

Not measured. The image builds and the harness is verified against a mock
endpoint; no GPU run has happened on this host yet. When it does, the numbers go
in `RESULTS.md` beside this file and the frontmatter above becomes
`state: benchmarked`.
