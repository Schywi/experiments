# Final résumé — AI infrastructure / platform engineering

```
LUCAS MIRANDA
AI Infrastructure & Platform Engineer
github.com/lucasmirandoliveira · lucasmirandoliveira@gmail.com · [city] / remote
```

## SUMMARY

Build AI operations systems that turn Kubernetes state into evidence an operator
can reproduce. Combine local LLM performance engineering, Cilium/Kubernetes
platform ownership, and least-privilege control design; publish the benchmark,
negative result, and design limit alongside the implementation.

## EXPERIENCE

**Independent Systems Engineer — AI Infrastructure & Platform** · *Sep 2026 – Oct 2026*

Built and measured a local LLM investigation system on native k3s: **184
commits**, **310 tracked files**, frozen benchmark baselines, and a public
failure catalogue.

- **Collapsed the normal investigation path from a per-tool LLM loop to two
  model stages** — measured on a frozen 27-question suite where deterministic
  bundles covered **17/27** intents and Laya planning reached **454 ms p50** —
  by separating intent planning, parallel evidence collection, and correlation.
  *(Retries can add calls; one 1.5B local model serves the path.)*

- **Demonstrated that a faster planner did not merit rollout** — measured across
  isolated Qwen and Laya arms at **31.4 s, 19.9 s, and 23.9 s** end to end,
  despite **18/27** versus **16/27** planner accuracy — by instrumenting every
  stage and showing correlation consumed **85–92%** of latency. *(One frozen run
  per arm; result is “no measurable end-to-end gain,” not a universal claim.)*

- **Turned LLM answers into reproducible operational evidence** — measured by
  provenance on every tool fact — by attaching exact arguments, timestamps,
  source systems, copyable `kubectl`/PromQL reproduction data, and verified
  Grafana/Hubble navigation outside the model context. *(No flow-level Hubble
  deep link exists; the assistant supplies a copyable command instead.)*

- **Root-caused a 143 s correlation outlier to host contention** — measured by
  prefill collapsing **16×** from **1,241 to 79 tok/s** while generation dropped
  only **13 to 9 tok/s** — by comparing co-scheduled Laya and llama.cpp workloads
  on a six-core host. *(Production correlate mean: 8.9 s at ~13 tok/s.)*

- **Recovered Cilium's full eBPF datapath after the k3d host rejected BPF SNAT**
  — measured by the failed sandboxed path and native-k3s configuration — by
  migrating to native k3s with kube-proxy replacement, Cilium LB-IPAM, and L2
  announcements. *(Single-node Fedora platform; no cloud or HA claim.)*

- **Built the Kubernetes device-integration layer for an unsupported AMD RX 570**
  — verified in source through `amd.com/render` registration and DRM allocation
  — by implementing the Go kubelet Device Plugin API (`ListAndWatch`,
  `Allocate`). *(Vulkan graphics/video only; no ROCm, measured GPU acceleration,
  or enforceable per-pod VRAM quota.)*

- **Blocked anonymous-admin regression in Argo CD's declarative reinstall path**
  — measured by the persisted Helm values and an install-time guard — by disabling
  the admin account, assigning anonymous callers `role:readonly`, pinning the
  LoadBalancer IP, and rejecting `server.disable.auth=true`.

## SELECTED PROJECT

**Bounded Replication Protocol** — *Go · Rust · Elixir · Kubernetes CRD*
**Separated a replication request from the authority to scale** — measured by
idempotent intent handling and a declared replica cap — by letting credentialless
workers emit one retry-safe intent while a Go controller deduplicates IDs and
alone patches `deployments/scale`. **0.35 MiB RSS; 467 KB static binary.**
*(Bounded replication by intent, not autoscaling or load balancing.)*

## TECHNICAL SKILLS

**AI systems:** llama.cpp · Qwen · tool use · structured-output contracts · local
inference · latency/token measurement · ONNX · TTS

**Platform:** Kubernetes (k3s, k3d) · Cilium/eBPF · Hubble · Helm · Argo CD ·
GitOps · RBAC · CRDs/controllers · Cilium LB-IPAM · Go Device Plugin API

**Observability:** Prometheus/PromQL · VictoriaMetrics · Grafana Explore ·
evidence provenance · reproducible benchmarks

**Languages:** Python · Go · Rust · Elixir · Bash
