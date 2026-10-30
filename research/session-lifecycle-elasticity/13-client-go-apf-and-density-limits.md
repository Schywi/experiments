# Note 13 — Client-go throughput, APF and Chromium density limits

| | |
|---|---|
| Status | Analysis and architecture findings |
| Date | 2026-10-29 |
| Phase | 8 — session lifecycle elasticity |
| Code | `apps/session-lab/internal/adapter/kube.go`, `apps/session-lab/cmd/pool-autoscaler/main.go` |
| Citations | `~/Documentos/git_repos/agent-sandbox` (`test/stress`, `cmd/agent-sandbox-controller`, `research/experiments/`) |

---

## 1. Problem definition: three distinct throughput layers

High-throughput session orchestration touches three decoupled layers:

| Layer | Mechanism | Control knob | Failure mode |
|---|---|---|---|
| Client submission | `client-go` token bucket rate limiter; HTTP/2 connection multiplexing | `cfg.QPS`, `cfg.Burst`, connection count | Client-side queueing delays, `client-side throttling` logs, HTTP/2 stream queueing |
| Control plane acceptance | API Priority and Fairness (APF), API server in-flight limits, etcd write path | `FlowSchema`, `PriorityLevelConfiguration`, `--max-mutating-requests-inflight` | `429 Too Many Requests`, HTTP 504, `connection refused` on `127.0.0.1:6443` |
| Workload materialization | Controller workers, kube-scheduler, kubelet runtime, Chromium launch | `MaxConcurrentReconciles`, pod resources, `--shm-size`, GPU rasterization | `FailedScheduling Insufficient cpu`, CFS throttling, memory cgroup OOM kills, CDP timeout |

Adjusting client-side settings addresses client submission. It does not alter server admission or container execution capacity.

---

## 2. Upstream implementations in agent-sandbox

The checkout at `~/Documentos/git_repos/agent-sandbox` documents and addresses these constraints across three subsystems:

### 2.1 Client rate limiter configuration

In `agent-sandbox`, client-side rate limiting is disabled to prevent the client token bucket from masking server-side flow control:

- `test/stress/main.go:323`:
  ```go
  restConfig.QPS = -1.0 // No client side rate-limiting
  ```
- `cmd/agent-sandbox-controller/main.go:121`:
  ```go
  flag.Float64Var(&kubeAPIQPS, "kube-api-qps", -1.0, "Client-side QPS limit for the Kubernetes API client (default: -1, no client-side rate limiting)")
  ```

When `QPS < 0`, `client-go` bypasses its internal token bucket limiter and immediately dispatches requests to transport.

### 2.2 HTTP/2 stream concurrency ceiling

Raising QPS or worker counts does not eliminate in-flight serialization because `kube-apiserver` enforces `SETTINGS_MAX_CONCURRENT_STREAMS` (default 100) per HTTP/2 connection. `client-go` multiplexes all requests from a single `rest.Config` over one TCP/TLS connection.

`agent-sandbox` addresses this by sharding mutating requests across dedicated HTTP/2 connections in `cmd/agent-sandbox-controller/transport.go:32-58` and `test/stress/clientconns.go`:

```go
// configureAPIConnections shards the controller's Kubernetes API traffic across
// `connections` independent TCP+TLS+HTTP/2 connections.
//
// Each shard gets a distinct Dial function, ensuring a distinct *http.Transport
// and distinct connection pool while maintaining HTTP/2 semantics.
```

### 2.3 API Priority and Fairness verification

`test/stress/apfcheck.go` executes a preflight dry-run `SandboxClaim` creation to verify APF classification. It reads the response headers:
- `X-Kubernetes-PF-FlowSchema-UID`
- `X-Kubernetes-PF-PriorityLevel-UID`

This confirms whether test traffic maps to the `exempt` PriorityLevel (via cluster-admin) or enters a queueing `FlowSchema`.

`test/stress/generate-report/templates/ratelimits.html:38-55` defines the two statistical signatures of client-go rate limiting:
- **Queueing**: Demand exceeds QPS, causing backlog growth and tail latency inflation.
- **Pacing**: Demand matches QPS, metering requests uniformly between `0` and `1/QPS` with a mean wait of `1 / (2 * QPS)`. Tail checks read normal while overall throughput drops proportionally.

---

## 3. Chromium density and resource boundaries

Measurements from `agent-sandbox/research/experiments/03-browser-density.md` and `02-capacity-6vcpu-100k.md` on a 6 vCPU / 14 GB free node establish the per-instance footprint:

### 3.1 Per-instance resource cost

| Metric | Measured value | Source |
|---|---|---|
| Processes per tab | 12 | `03-browser-density.md:186` |
| Pod cgroup `memory.current` | 559 MiB | `03-browser-density.md:187` |
| `anon` memory | 410.9 MiB | `03-browser-density.md:188` |
| `file` memory | 131.0 MiB | `03-browser-density.md:189` |
| `gpu-process` SwiftShader CPU | ~1.0 core | `03-browser-density.md:196` |
| Adoption from WarmPool | 92 ms p50 | `04-workload-archetypes.md` |
| Cold start creation | 41–50 s p50 | `04-workload-archetypes.md` |

### 3.2 Sequence of system boundaries under load

1. **Scheduler CPU request exhaustion**:
   With CPU requests at 100m–500m per pod, requests exhaust 6 vCPU allocatable capacity while node physical memory is still >50% available. `02-capacity-6vcpu-100k.md` recorded `FailedScheduling 1 Insufficient cpu` at 5,970m / 6,000m.
2. **Control plane saturation**:
   Bursts of claim creations and pool reconciliations overwhelm local `k3s` apiserver. Recorded in Note 09 and Note 10 as `dial tcp 127.0.0.1:6443: connect: connection refused` and `publish settings: unexpected EOF` at 22–40 replicas.
3. **Memory compression and swap cost**:
   Anonymous pages exhaust RAM, forcing zram compression. CPU spent on page compression directly competes with SwiftShader browser rendering.
4. **Responsiveness violation**:
   Browser CDP round-trip exceeds 2000 ms p95 despite the pod maintaining `Ready=True`.

---

## 4. Current state in session-lab

In `apps/session-lab/internal/adapter/kube.go:63-64`:

```go
cfg.QPS = 200
cfg.Burst = 400
```

This raises the token bucket from the default `5.0 / 10`, but retains a single HTTP/2 connection. During autoscaler ramp tests (Note 10), 16 claims were stranded when the apiserver crashed, leading to cascading failures.

---

## 5. Metrics for control-plane capacity analysis

Prometheus queries to evaluate control-plane saturation during load:

```promql
# API request rate by verb, resource, and status code
sum by (verb, resource, code) (rate(apiserver_request_total[5m]))

# API request p99 duration
histogram_quantile(0.99, sum by (le, verb, resource) (rate(apiserver_request_duration_seconds_bucket[5m])))

# In-flight requests against apiserver limits
max by (request_kind) (apiserver_current_inflight_requests)

# APF rejections by schema and priority level
sum by (flow_schema, priority_level) (rate(apiserver_flowcontrol_rejected_requests_total[5m]))

# Workqueue depth for the sandbox controller
workqueue_depth{name="sandbox-controller"}
```

---

## 6. Implementation architecture for high throughput

To achieve sustainable high-throughput sandbox leasing without control-plane outages:

1. **Client rate limiting**:
   Set `cfg.QPS = -1.0` in load testing harnesses (`session-lab`) when benchmark intent is measuring cluster capacity. For production services, enforce admission bounds via internal application workqueues rather than relying on client-go token bucket drops.
2. **Transport connection sharding**:
   Shard mutating operations across 4–8 HTTP/2 connections via custom `rest.Config.WrapTransport` when concurrent worker counts exceed 100.
3. **Dedicated priority levels**:
   Define explicit `FlowSchema` and `PriorityLevelConfiguration` objects for sandbox leasing controllers to isolate claim processing from background telemetry and node heartbeats.
4. **Decouple CPU limits from requests**:
   Apply Note 12 (`requests: 100m`, no CPU limit) to eliminate artificial CFS throttling during Chromium tab initialization.
