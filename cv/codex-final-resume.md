# Lucas Miranda

```
Infrastructure & Platform Engineer
github.com/lucasmirandoliveira · lucasmirandoliveira@gmail.com · [city] / remote
```

## PROFILE

Build infrastructure software that helps operators inspect Kubernetes systems
and verify what they see. Work spans local language model integration, platform
networking, access controls, and performance measurement. Publish source,
benchmarks, and known limits.

## EXPERIENCE

**Independent Systems Engineer, Infrastructure & Platform** | *Sep 2026 to Oct 2026*

Built and measured a local LLM investigation system on native k3s: **184
commits**, **310 tracked files**, frozen benchmark baselines, and a public
failure catalogue.

- Replaced a sequential tool-and-model loop with a two-stage workflow. On a
  frozen 27-question benchmark, keyword bundles handled **17/27** questions
  without a planning call; Laya planner latency was **454 ms p50**. The code
  separates planning, parallel tool calls, and answer generation. *(Retries can
  add calls; one local 1.5B model serves the workflow.)*

- Avoided adopting a separate planner that did not improve total response time.
  In the frozen comparison, end-to-end means were **31.4 s, 19.9 s, and 23.9 s**;
  Laya scored **18/27** versus Qwen's **16/27**, while answer generation consumed
  **85–92%** of latency. *(One run per arm; no measurable end-to-end gain.)*

- Made each investigation result checkable by attaching tool arguments, time,
  source, and copyable `kubectl` or PromQL commands. Server-generated Grafana
  and Hubble links sit outside model input. *(Hubble flow results include a
  command because its UI has no flow-level link.)*

- Traced a **143 s** outlier to CPU contention on a six-core host: prefill fell
  **16×**, from **1,241 to 79 tok/s**, while generation fell from **13 to 9
  tok/s** when Laya and llama.cpp ran together. *(Production answer-generation
  mean was 8.9 s at about 13 tok/s.)*

- Restored Cilium eBPF networking after the k3d kernel rejected BPF SNAT by
  moving the lab to native k3s with kube-proxy replacement, Cilium LB-IPAM, and
  L2 announcements. *(Single-node Fedora host.)*

- Wrote a Go kubelet Device Plugin for an AMD RX 570 without a vendor plugin.
  It registers `amd.com/render` and allocates DRM render nodes through
  `ListAndWatch` and `Allocate`. *(Vulkan graphics and video only; no ROCm,
  GPU speedup result, or per-pod VRAM limit.)*

- Prevented an Argo CD reinstall from restoring anonymous admin access by
  persisting read-only anonymous RBAC, disabling the admin account, pinning the
  LoadBalancer address, and adding an install guard for
  `server.disable.auth=true`.

## SELECTED PROJECT

**Bounded Replication Protocol** | *Go, Rust, Elixir, Kubernetes CRD*

Designed workers to request one replica through an idempotent intent while a Go
controller alone changes Deployment scale, deduplicates intent IDs, and enforces
`maxReplicas`. Worker footprint measured at **0.35 MiB RSS** and **467 KB** for
the static binary. This is bounded replication by intent.

## TECHNICAL SKILLS

**Language models:** llama.cpp · Qwen · tool calling · structured output · local
inference · latency measurement · ONNX · text-to-speech

**Platform:** Kubernetes (k3s, k3d) · Cilium/eBPF · Hubble · Helm · Argo CD ·
GitOps · RBAC · CRDs/controllers · Cilium LB-IPAM · Go Device Plugin API

**Monitoring:** Prometheus/PromQL · VictoriaMetrics · Grafana Explore ·
reproducible benchmarks

**Languages:** Python · Go · Rust · Elixir · Bash
