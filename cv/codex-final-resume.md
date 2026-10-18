# Lucas Miranda

**AI Infrastructure & Platform Engineer**<br>
Local LLM systems · Kubernetes · observability · Cilium · Go<br>
github.com/lucasmirandoliveira · lucasmirandoliveira@gmail.com · [Location / remote]

## Summary

Systems engineer who builds evidence-backed infrastructure tooling and the
Kubernetes platform beneath it. Developed a local LLM investigation assistant
with bounded read-only tools, reproducible evidence, and frozen performance
experiments; also built the native k3s, Cilium, GitOps, and edge-GPU integration
needed to run it. Public repository: every stated metric traces to source or a
dated benchmark.

## Experience

**Independent Systems Engineer — AI Infrastructure & Platform**<br>
*Self-directed public engineering program · Sep 2026 – Oct 2026*

- Replaced a sequential per-tool LLM loop with a **plan → concurrent fan-out →
  correlate** pipeline, making the normal free-form path two model stages; a
  frozen 27-question set used deterministic zero-plan-call bundles for **17/27**
  intents, while Laya planning measured **454 ms p50**.
- Built an evidence-first Kubernetes investigation assistant with **10 read-only
  tools**, typed risk levels, JSONL audit records, and read RBAC limited to
  `get/list/watch`; separate confirmation-gated actions use single-use **120 s**
  tokens and patch-only permissions.
- Made investigation outputs independently checkable by attaching arguments,
  timestamp, source, reproducible `kubectl`/PromQL commands, and verified
  Grafana/Hubble navigation server-side, preserving the model context budget.
- Treated performance as an experiment: a frozen three-arm comparison found that
  a planner with **18/27** accuracy versus **16/27** did not improve end-to-end
  latency because correlation consumed **85–92%** of each run. Published the
  negative result and its one-run-per-arm caveat.
- Diagnosed a **143 s** latency outlier as CPU contention rather than model speed:
  co-scheduling reduced prefill from **1,241 to 79 tok/s** while generation fell
  from **13 to 9 tok/s**; production correlation averaged **8.9 s** at ~13 tok/s.
- Migrated from Docker-backed k3d to native k3s after the host rejected Cilium
  BPF SNAT, restoring the eBPF datapath and rebuilding exposure with Cilium
  LB-IPAM and L2 announcements.
- Implemented a Go kubelet **Device Plugin** for an unsupported AMD RX 570:
  `ListAndWatch` advertises `amd.com/render`, and `Allocate` mounts DRM render
  nodes. Scoped the claim to Vulkan graphics/video; ROCm compute and per-pod
  VRAM quotas are unsupported on this hardware.
- Prevented an Argo CD reinstall from restoring anonymous admin access by
  persisting anonymous read-only RBAC, disabling the admin account, pinning a
  Cilium LoadBalancer IP, and adding an install-time guard against
  `server.disable.auth=true`.

## Selected Project

**Bounded Replication Protocol** — *Go · Rust · Elixir · Kubernetes CRD*<br>
Designed a visible replication-by-intent experiment: workers hold no Kubernetes
credentials, submit idempotent intents, and a Go controller alone scales the
Deployment while deduplicating IDs and enforcing `maxReplicas`. Measured worker
footprint: **0.35 MiB RSS** and a **467 KB** static binary. This is bounded
replication, not autoscaling or load balancing.

## Technical Skills

**AI systems:** local inference (llama.cpp/Qwen), tool use, structured-output
contracts, token/latency measurement, ONNX, TTS<br>
**Platform:** Kubernetes (k3s, k3d), Cilium/eBPF, Hubble, Helm, Argo CD, GitOps,
RBAC, CRDs/controllers, Cilium LB-IPAM<br>
**Observability:** Prometheus/PromQL, VictoriaMetrics, Grafana Explore,
provenance, reproducible benchmarking<br>
**Languages:** Python, Go, Rust, Elixir, Bash

## Evidence Available in the Public Repository

Frozen 27-question benchmarks; negative E2 planner experiment; edge-case
catalogue; Device Plugin source; Argo CD configuration history; and documented
limits for single-node operation, unsupported ROCm compute, and model-output
degradation.
