# Note 12 — CPU: requests yes, limits no

| | |
|---|---|
| Status | Policy, confirmed against the live cluster |
| Date | 2026-10-29 |
| Applies to | `config/sandbox/` (browser `SandboxTemplate`), `config/session-lab/` (autoscaler) |
| Reference | *For the Love of God, Stop Using CPU Limits on Kubernetes* — https://home.robusta.dev/blog/stop-using-cpu-limits |
| Evidence | 15 run records under `docs/session-lab-runs/`, live `SandboxTemplate`, live Deployment |

---

## 1. The policy

**CPU requests: yes. CPU limits: no.**

- A **request** is what the scheduler reserves and what the kernel's CPU shares are derived from. It
  guarantees the pod gets its share under contention, and it is the number capacity planning uses.
- A **limit** is a CFS quota. Once the container reaches it inside a 100 ms period, the kernel stops
  scheduling it until the next period, no matter how idle the rest of the node is. The consequence
  is latency that comes from the configuration rather than from the workload.

CPU is compressible: if a pod has no quota it competes on shares and simply runs slower when the
node is busy, while the node's idle capacity remains usable. That is the behaviour a sandbox wants —
a browser that briefly needs 1.5 cores should get them, because nothing else is asking for them.

**Memory keeps its limit.** Memory is incompressible: without a limit one pod grows until the node
OOMs and takes unrelated workloads with it. `memory: 768Mi` stays.

## 2. What is configured today

Read from the live objects, not from the chart:

| Object | CPU request | CPU limit | Memory request | Memory limit |
|---|---|---|---|---|
| `sandboxtemplate/browser-tpl` → container `browser` | **100m** | **1 (1000m)** | 256Mi | 768Mi |
| `deployment/session-lab-autoscaler` (chart values) | 20m | 200m | 32Mi | 128Mi |

Chart sources: `config/sandbox/values.yaml` (`sandboxes.template.resources`) and
`config/session-lab/values.yaml` (`resources`).

So the estate is **not** compliant with the policy in §1: two CPU limits exist.

## 3. Why this matters here, measured

The runner records CFS throttling per session from `cpu.stat` (`nr_throttled`, `throttled_usec`).
Across the 15 runs on this branch:

| | |
|---|---|
| sessions recorded | 360 |
| sessions CPU-throttled at some point | **248 (69%)** |
| worst single session | 2.75 s of throttled time (`…162748Z`, 8 sessions of 20 s) |
| worst at load | 1.35 s in a 7.3-minute 60-session run (`…161101Z`) |

Every report prints the limit next to the count, which is how this stayed visible:

```
template browser      requests cpu=100m mem=256MiB, limits cpu=1000m mem=768MiB
throttled sessions 43, oom kills 0, sessions with refaults 40
```

Those sessions' hold and queue latencies are shaped by a quota, not by the workload. Any capacity
number produced under a throttle-heavy run understates what the hardware can do.

## 4. The change that complies (not applied)

Removing the limit is a two-line edit, both in chart values:

```yaml
# config/sandbox/values.yaml  (sandboxes.template.resources)
      limits:
-       cpu: "1"
        memory: 768Mi

# config/session-lab/values.yaml  (resources)
    limits:
-     cpu: 200m
      memory: 128Mi
```

What that changes, and what it does not:

| Effect | Before | After |
|---|---|---|
| `cpu.max` in the pod cgroup | `100000 100000` | `max 100000` |
| Throttling under load | throttled seconds accumulate | none — the pod competes on shares |
| Scheduling guarantee | 100m reserved | unchanged, still 100m |
| Per-pod CPU ceiling | 1 core | none; the node's own capacity is the ceiling |
| Failure mode under a runaway page | latency from the quota | neighbour latency from CPU contention |
| Memory ceiling | 768Mi + OOM | unchanged |

The trade-off is real and belongs in the record: without a CPU limit a Chromium that pegs all six
cores will slow every other pod on the node, including the API server — the failure this cluster has
already met at high concurrency. Requests plus the node's capacity are the remaining controls, and
the autoscaler's `maxReplicas` bounds how many such pods can exist.

## 5. How to verify after the change

```bash
# the pod has no quota (expect "max 100000")
uid=$(kubectl -n sandbox-demo get pod <a browser pod> -o jsonpath='{.metadata.uid}')
cat /sys/fs/cgroup/kubepods.slice/*/*pod${uid//-/_}*.slice/cpu.max

# no throttling accumulates during a session
rg '^(nr_throttled|throttled_usec)' /sys/fs/cgroup/kubepods.slice/*/*pod${uid//-/_}*.slice/cpu.stat

# and the runner's own report says zero
sqlite3 docs/session-lab-runs/<run>.db \
  "select count(*) from sessions where throttled_usec > 0;"   # expect 0
```

A run whose report still shows `throttled sessions` above zero means a limit is still in place —
either the chart was not re-synced or another object sets it.

## 6. Where this policy is written down

- `config/sandbox/values.yaml` — comment above the `resources` block.
- `apps/session-lab/README.md` — the report prints requests and limits together with the throttle
  count, which is the signal that a limit is biting.
