---
title: 'Session queue and pod multiplexing audit: acquire latency breakdown'
kind: audit
state: validated
phase: 8
date: 2026-11-07
branch: 'research/session-lifecycle-elasticity'
tags: [session-lifecycle, queue, multiplexing, latency, state-validated]
---

# Note 22 — Session queue and pod multiplexing audit: acquire latency breakdown

| | |
|---|---|
| Status | Validated audit of run telemetry and runner queue architecture |
| Date | 2026-11-07 |
| Phase | 8 — session lifecycle elasticity |
| Branch | `docs-site` |
| Related | [16.1 browser contexts and slots](16.1-browser-contexts-and-slots.md), [18 capacity elasticity and kine](18-capacity-elasticity-and-kine.md), [21 manual scaling runbook](21-manual-scaling-runbook.md) |

---

## 1. Workload and Telemetry Intake

A high-rate stress run was executed against the browser cell to measure concurrency limits under rapid arrival:

* **[M] Workload parameters**: 3,000 total sessions at 35 sessions/second with `--policy reuse`, `--contexts-per-browser 8`, on a single bare-metal host (6 cores Intel i5-9400F, 16 GiB RAM + 16 GiB zram swap).
* **[M] Telemetry results**:
  * Outcomes: 2,992 success, 8 failed (socket exhaustion).
  * In-flight concurrency: peaked at 2,969 simultaneous sessions.
  * Active sandboxes: 38 browser pods active (autoscaler cap configured at 80).
  * Queue Wait (`session.ReadyNS - session.ClaimCreatedNS`): p50 0 ms, p95 0 ms, p99 6.2 s, max 302 s.
  * Acquire Latency (`session.ReadyNS - session.ArrivalNS`): p50 1,325,325 ms (~22 min), p95 2,191,569 ms (~36 min).
  * Instance density: max 114 sessions/instance, mean 78.95 sessions/instance.
  * Resource usage: CPU mean 1.97 / 6 cores; RAM dropped to 10.9 % free; swap peaked at ~7.7 GiB.

---

## 2. Telemetry Discrepancy Analysis

### 2.1 The Concurrency Math: Cumulative Throughput vs Instant Density

* **[D]** The runner reports `Instance Density: mean 78.95 sessions/instance`. In `apps/session-lab/internal/runtime/session/shared.go:249`, `lease.Instance.Sessions++` increments on session completion. This metric measures **cumulative lifetime throughput** per pod over the entire 36-minute window:
  $$\frac{3,000\text{ completed sessions}}{38\text{ pods}} = \mathbf{78.95\text{ sessions / instance}}$$
* **[D]** Instantaneous concurrent contexts per Chromium instance never exceeded **8**. This ceiling is hard-enforced in `apps/session-lab/internal/runtime/slots/slots.go:100` (`p.perInstance = contextsPerBrowser`). Chromium was never running 79 sessions concurrently.

### 2.2 Why Scaling Stopped at 38 Pods Instead of 80

The platform reached two independent limits before hitting the autoscaler's 80-pod cap:

1. **Host Memory Ceiling [M]**:
   * Each Chromium pod with 8 active contexts holds ~350–450 MiB resident anonymous memory.
   * At 38 pods, active memory was:
     $$14.3\text{ GiB (RAM)} + 7.7\text{ GiB (zram swap)} = \mathbf{22.0\text{ GiB total allocation}}$$
   * Physical RAM was 89.1 % consumed (10.9 % free). Scaling to 80 pods would demand $80 \times 400\text{ MiB} \approx 32\text{–}35\text{ GiB}$, which exceeds the physical RAM + zram envelope (16 GiB + 16 GiB) and would trigger kernel OOM panics.
2. **Serialized Pod Provisioning in the Runner [D]**:
   * In `apps/session-lab/internal/runtime/slots/slots.go:76-88`, `p.provisioning` gates instance acquisition:
     ```go
     if p.provisioning > 0 {
         wait := p.wait
         p.mu.Unlock()
         select {
         case <-wait:
             continue
         }
     }
     p.provisioning++
     ```
   * The slot pool provisions **exactly one pod at a time**. It does not issue batch claims to Kubernetes. While pod $N$ boots and waits for Kubernetes readiness (5–12 s), all other waiting sessions block on `<-wait`.

---

## 3. Queue Wait vs Acquire Latency Breakdown

* **[D] Metric definitions** (`apps/session-lab/internal/runtime/report/report.go:231-232`):
  * `Queue Wait` = `session.ReadyNS - session.ClaimCreatedNS`
  * `Acquire Latency` = `session.ReadyNS - session.ArrivalNS`
* **[I] Where sessions sat for 22–36 minutes**:
  * At 35 sessions/sec, all 3,000 sessions registered their `ArrivalNS` in the first $\frac{3,000}{35} \approx \mathbf{85.7\text{ seconds}}$.
  * With 38 pods and 8 slots each, total active concurrency is $\mathbf{304\text{ slots}}$.
  * The remaining $\sim 2,696$ sessions remained blocked as sleeping goroutines waiting for available slots inside `spool.Take()`.
  * With an average hold duration of $\sim 120\text{ s}$, Little's Law dictates draining 3,000 sessions through 304 slots requires:
    $$\frac{3,000\text{ sessions} \times 120\text{ s}}{304\text{ slots}} \approx 1,184\text{ seconds} \approx \mathbf{19.7\text{ to } 36\text{ minutes}}$$
* **[I] Why Queue Wait was 0 ms**:
  * `session.ClaimCreatedNS` is recorded in `sessionShared` only when a slot is actively requested from `spool.Take()` (`shared.go:164`).
  * Once a previously queued session is granted an already-warm slot from an idle context on an existing pod, `takeFreeLocked()` assigns the slot in microseconds:
    $$\text{ReadyNS} - \text{ClaimCreatedNS} \approx \mathbf{0\text{ ms (p50 / p95)}}$$
  * Only sessions that triggered cold pod creation waited on the Kubernetes pod lifecycle (accounting for the max 302 s queue outlier).

---

## 4. Architectural Findings and Recommendations

1. **Multiplexing (8 contexts / pod) is essential [I]**:
   Attempting 3,000 individual unmultiplexed pods on a 6-core / 16 GiB node would require $> 900\text{ GiB}$ of memory. Multiplexing prevented node collapse.
2. **Decoupled Queue Visibility [D]**:
   The in-cluster `session-lab-autoscaler` inspects `SandboxClaim` CRDs (`stats.Pending`). Because `slots.Pool` serializes provisioning to one claim at a time, the cluster autoscaler observed `Pending = 0` or `1`, entirely unaware of the $> 2,600$ sessions waiting in the runner's Go channels.
3. **Metrics to Monitor Before Queue Saturation**:
   * **Slot Utilization Ratio**: $\frac{\text{Active Contexts}}{\text{Ready Pods} \times \text{ContextsPerBrowser}}$. Alerts when $> 0.85$.
   * **Runner Go Waiter Depth**: Number of goroutines blocked on `spool.Take()` (`len(p.wait)`).
   * **Memory PSI (`/proc/pressure/memory`)**: Tracks when zstd swapping begins competing with Chromium context execution.
