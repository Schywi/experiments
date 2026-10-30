# Note 14 — Leaked claims, the 100ms polling storm, and claim ordering

| | |
|---|---|
| Status | Analysis and lifecycle documentation |
| Date | 2026-10-29 |
| Phase | 8 — session lifecycle elasticity |
| Code | `apps/session-lab/internal/adapter/kube.go`, `apps/session-lab/internal/run/run.go`, `scripts/session-lab-experiment.sh` |
| Citations | `~/Documentos/git_repos/agent-sandbox` (`extensions/controllers/sandboxclaim_controller.go`, `extensions/api/v1beta1/sandboxclaim_types.go`, `research/features/03-two-archetype-examples.md`) |

---

## 1. The 100ms polling storm in `WaitReady`

In `apps/session-lab/internal/adapter/kube.go:107-137`, adoption detection is implemented as a polling loop:

```go
func (k *Kube) WaitReady(ctx context.Context, instance *Instance) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		claim, err := k.dyn.Resource(gvrClaims).Namespace(instance.Namespace).Get(ctx, instance.Claim, metav1.GetOptions{})
...
```

### 1.1 The arithmetic of the storm

- **Per-session rate**: 1 `Get` every 100 ms = **10 requests/s per waiting session**.
- **At 20 concurrent sessions waiting**: $20 \times 10 = \mathbf{200\text{ req/s}}$.
- **At 50 concurrent sessions waiting**: $50 \times 10 = \mathbf{500\text{ req/s}}$.

With `cfg.QPS = 200` and `cfg.Burst = 400` in `kube.go:63-64`, once 20 or more sessions are simultaneously waiting for readiness (such as during pool depletion or pod cold-starts), the polling loop exhausts 100% of the client token bucket.

Subsequent operations—including `Create`, `fillFromSandbox` lookups, and `DELETE` cleanups—are throttled by `client-go`'s rate limiter, injecting multi-second client-side delays.

### 1.2 Origin and remedy

- **Why 100ms was chosen**: Warm pool adoption latency is ~92 ms p50. The tight interval was chosen to capture the timestamp without adding multi-second latency artifacts.
- **The remedy**: The idiomatic Kubernetes mechanism is a `Watch` (or shared Informer cache). An API server `Watch` streams status transitions over a single persistent HTTP/2 connection when `Ready=True`, generating zero polling requests.

---

## 2. Leaked claims: origin and operational consequences

A `SandboxClaim` requests a sandbox from a pool (`spec.warmPoolRef.name`). A claim leaks when its client process terminates without deleting the object from Kubernetes etcd.

### 2.1 Causes of leaked claims

1. **Uncaught signals and script aborts**: If `session-lab` is terminated via `SIGINT` (Ctrl+C), `SIGTERM`, or a shell error under `set -e`, the process terminates before `releaseAll()` (`run.go:706`) can execute.
2. **Control-plane dropouts**: When the local k3s API server drops connections under load (`dial tcp 127.0.0.1:6443: connect: connection refused`), the 5-attempt retry loop in `deleteWithRetry` (`run.go:687`) fails, leaving claims in etcd.

### 2.2 System consequences

- **Phantom demand**: The `agent-sandbox-controller` treats all active `SandboxClaim` objects as legitimate pending demand.
- **Resource starvation**: The controller attempts to fulfill stranded claims by provisioning `Sandbox` objects and child `Pod` instances, consuming node CPU shares and memory requests.
- **Autoscaler pollution**: In Note 10, 16 leaked claims held the WarmPool autoscaler at 22 replicas indefinitely with zero actual workload traffic.

---

## 3. Claim reconciliation ordering: old claims vs. new claims

When a cluster holds unfulfilled claims created 1 hour ago alongside newly created claims:

### 3.1 Non-FIFO workqueue semantics

In Kubernetes `controller-runtime`, reconciler queues (`workqueue.TypedRateLimitingInterface`) are **not** global FIFO queues ordered by `metadata.creationTimestamp`:

- A claim failing adoption enters exponential rate-limiting backoff.
- A newly created claim generates an immediate `ADD` event, placing it at the front of the active worker queue.
- If a warm sandbox becomes available while an older claim is waiting in its backoff delay window, the **new claim adopts the warm sandbox first**.

### 3.2 Capacity hijacking by abandoned claims

If an abandoned 1-hour-old claim exits its backoff delay at the moment a warm sandbox is replenished, the controller binds that warm sandbox to the abandoned claim. The active user who just submitted a request is starved of the warm pool and forced into a cold start.

---

## 4. Upstream analysis (`agent-sandbox`)

Inspection of `~/Documentos/git_repos/agent-sandbox` shows how lifecycle is handled:

### 4.1 Implemented features

- `spec.lifecycle.shutdownTime` (`extensions/api/v1beta1/sandboxclaim_types.go:71`): Absolute clock time at which the controller terminates the backing `Sandbox`.
- `spec.lifecycle.ttlSecondsAfterFinished` (`sandboxclaim_types.go:77`): TTL duration starting from the `Finished` condition timestamp. When expired, the controller deletes the `SandboxClaim`.
- `spec.lifecycle.shutdownPolicy: DeleteForeground` (`sandboxclaim_types.go:82`): Cascades deletion down to the child sandbox and pods upon expiration.

### 4.2 The pending claim gap

Upstream documentation in `research/features/03-two-archetype-examples.md:168-170` notes:
> *"A Sandbox has no `activeDeadlineSeconds` — a runaway process inside it is bounded only by `shutdownTime`/TTL, and **TTL is measured from finish, which a runaway never reaches.**"*

Because `ttlSecondsAfterFinished` is measured from the `Finished` condition, an abandoned claim that never completed adoption remains in `Pending` indefinitely. No native admission deadline or lease timeout exists on `SandboxClaim`.

---

## 5. Session reuse vs. deletion

The runner in `apps/session-lab/internal/run/run.go:504-548` implements two operational paths:

1. **`--policy reuse`**:
   - Executes: `Session A (example.com) -> CDP Page.navigate(about:blank) -> Session B`.
   - Reset completes in **35–39 ms p50** without terminating the container.
   - Deletion occurs only at run completion via `releaseAll()`.
2. **`--policy destroy`**:
   - Executes: `Session A -> Delete SandboxClaim -> Pod termination -> Cold start / Warm pool refill -> Session B`.
   - Avoids persistent claim leakage across sessions, but pays 374 ms p50 deletion latency plus container initialization delays.

---

## 6. Prevention and mitigation strategies

1. **Shell exit trap**:
   In `scripts/session-lab-experiment.sh`, register a trap to ensure cleanup on normal exit, `SIGINT`, or error:
   ```bash
   cleanup() {
     local exit_code=$?
     kubectl -n "${namespace}" delete sandboxclaims -l app.kubernetes.io/managed-by=session-lab --wait=false 2>/dev/null || true
     kubectl -n "${namespace}" delete sandboxes -l app.kubernetes.io/managed-by=session-lab --wait=false 2>/dev/null || true
     exit "${exit_code}"
   }
   trap cleanup EXIT INT TERM
   ```
2. **Bounded lease deadlines**:
   Pass `--session-deadline` to `session-lab` so waiting claims are cancelled client-side rather than waiting indefinitely.
3. **Automated stale claim reaper**:
   Deploy a periodic cron or controller that identifies unfulfilled `SandboxClaim` objects where `metadata.creationTimestamp` exceeds a defined threshold (e.g., 15 minutes) and deletes them.
