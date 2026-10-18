# Final resume — master AI infrastructure / platform version

```
LUCAS MIRANDA
AI Infrastructure & Platform Engineer
github.com/lucasmirandoliveira · lucasmirandoliveira@gmail.com · [city] / remote
```

## SUMMARY

Systems engineer who builds local AI operations tooling and the Kubernetes
platform beneath it: evidence-backed LLM investigation, Cilium networking,
least-privilege control paths, and measured system behaviour. Public work with
frozen benchmarks, source-level traceability, and documented limits.

## EXPERIENCE

**Independent Systems Engineer — AI Infrastructure & Platform** · *Self-directed public engineering program · Sep 2026 – Oct 2026*

Built and measured a native-k3s platform and local LLM infrastructure assistant:
**184 commits**, **310 tracked files**, frozen benchmark baselines, and a public
failure catalogue.

- **Replaced a per-tool LLM loop with a two-stage investigation pipeline** —
  measured on a frozen 27-question suite, where deterministic bundles handled
  **17/27** intents without a planning call and Laya planning reached **454 ms
  p50** — by separating plan, concurrent tool fan-out, and correlation.
  *(Normal path is two model stages; retry paths can make additional calls.)*

- **Established that a faster planner did not improve end-to-end latency** —
  measured across three isolated arms (**31.4 s → 19.9 s → 23.9 s**; **18/27**
  versus **16/27** planner accuracy) — by isolating Qwen and Laya under
  Guaranteed QoS and recording every stage. *(One frozen run per arm; correlation
  remained 85–92% of latency.)*

- **Made infrastructure answers independently reproducible** — measured by
  server-side evidence attached to every tool fact — by returning the exact
  arguments, timestamp, source, `kubectl`/PromQL reproduction data, and verified
  Grafana or Hubble navigation outside the model context. *(Flow evidence uses a
  copyable Hubble command because the deployed UI has no flow-level deep link.)*

- **Diagnosed a 143 s correlation outlier as host contention, not model speed**
  — measured by a **16×** prefill collapse (**1,241 → 79 tok/s**) versus a
  **2×** generation decline (**13 → 9 tok/s**) — by comparing the co-scheduled
  Laya and llama.cpp workloads on a six-core node. *(Production correlate mean:
  8.9 s at about 13 tok/s.)*

- **Migrated the lab from k3d to native k3s to restore Cilium's eBPF datapath**
  after the Docker-backed kernel rejected BPF SNAT — by disabling the k3s
  bundled network components and using Cilium kube-proxy replacement, LB-IPAM,
  and L2 announcements. *(Single-node Fedora host, not a cloud or HA platform.)*

- **Implemented the Kubernetes device-integration layer for an AMD RX 570** —
  verified in source by registration of `amd.com/render` and DRM-node allocation
  — by writing a Go kubelet Device Plugin with `ListAndWatch` and `Allocate`.
  *(Vulkan graphics/video only: no ROCm compute, measured GPU speedup, or
  per-pod VRAM quota.)*

- **Hardened Argo CD's declarative reinstall path against anonymous admin** —
  measured by the persisted Helm values and install guard — by disabling the
  admin account, assigning anonymous callers `role:readonly`, pinning the
  Cilium LoadBalancer address, and rejecting `server.disable.auth=true`.

## SELECTED PROJECT

**Bounded Replication Protocol** — *Go · Rust · Elixir · Kubernetes CRD*
Designed a replication-by-intent protocol where unprivileged workers send one
idempotent request and a Go controller alone scales the Deployment, deduplicates
intent IDs, and clamps growth to `maxReplicas`. **0.35 MiB RSS**, **467 KB**
static binary. Bounded replication by intent, not autoscaling or load balancing.

## TECHNICAL SKILLS

**AI systems** llama.cpp · Qwen · tool use · structured-output contracts · local
inference · latency/token measurement · ONNX · TTS

**Platform** Kubernetes (k3s, k3d) · Cilium/eBPF · Hubble · Helm · Argo CD ·
GitOps · RBAC · CRDs/controllers · Cilium LB-IPAM · Go Device Plugin API

**Observability** Prometheus/PromQL · VictoriaMetrics · Grafana Explore ·
evidence provenance · reproducible benchmarks

**Languages** Python · Go · Rust · Elixir · Bash
