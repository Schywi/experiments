# Note 18 — Capacity, elasticity, and why kine is the ceiling

| | |
|---|---|
| Status | Analysis with live evidence, 2026-10-31 |
| Phase | 8 — session lifecycle elasticity |
| Evidence | 600-session run `20261031T024806Z`, k3s journal, apiserver `/metrics`, node allocation |
| Follows | [16 — browser contexts and slots](16-browser-contexts-and-slots.md), [17 — a CDP client in Go](17-cdp-client-in-go.md) |
| Tracked as | `experiments-0fo` kine metrics, `experiments-9b0` dashboard, `experiments-ujf` vocabulary, `experiments-wnj` elasticity, `experiments-9kl` control-plane writes, `experiments-c4h` capacity ladder, `experiments-kvx` memory gauge |

---

## 1. Four names for four things

The report said "instances 21, sessions/instance max 37" and "30 of 30 sessions shared a
browser" while also claiming 8 contexts per browser. Four levels were being called by
overlapping names. They are distinct:

```
SandboxClaim        one capacity request; adopts a warm pod or cold-starts one
   └── Sandbox/Pod  one Chromium process tree, one cgroup, one network namespace
         └── slot  one browser context (Target.createBrowserContext)
               └── session  one lease of a slot for a hold duration
```

| Level | Unit of | Counted by | Lifetime | Who owns it |
|---|---|---|---|---|
| claim | capacity | `ClaimStats` (ready/pending/failed/bulk) | minutes to hours | the autoscaler asks, the platform creates |
| pod | compute | `instances` in the report, cgroup | minutes to hours | pool / claim |
| slot | isolation | `--contexts-per-browser` per instance | seconds | `runtime/slots` |
| session | work | `sessions` in the report | 30 s – 5 min | `runtime/session` |

Rules that follow, and that the code already obeys:

1. A slot carries **exactly one** session at a time; two sessions never share a context.
2. An instance carries **up to N** slots, N = `--contexts-per-browser` (default 1).
3. A session does **not** map to a pod when N > 1. "Sessions per instance 37" means *37
   sessions ran on that browser during the run*, not 37 at once — the ceiling was 8.
4. Per-session CPU and memory are a **share of the pod** (`cpu_usec`, `mem_peak_bytes`
   divided by the contexts in flight, `cpu_shared=true`). Per-context measurements are
   separate quantities: `task_duration_us`, `script_duration_us`, `js_heap_bytes`.
5. The autoscaler sizes **instances** from **slot demand**: `ceil((ready+pending)/N)`.

## 2. Is 600 the maximum? No — it is not a limit at all

600 sessions in 10 minutes is 1.0 session/s average, or 2.0/s offered. The run did not hit one
ceiling, it hit three in sequence, and one of them was a bug of mine (the watch options, fixed
in `ae04cf4`):

| Constraint | Limit on this host | Evidence in that run |
|---|---|---|
| Scheduler (CPU requests) | ~21 browsers × 8 = **168 slots**; 27 if browsers had no neighbours | `FailedScheduling … Insufficient cpu` at 23:59 with 21 instances |
| Node memory | 15.9 GiB; 21 browsers ≈ 7.3 GiB + system | `min available 2846 MiB (17.9%)`, swap 4137 MiB |
| Control plane (kine) | ~2 sessions/s of write churn | apiserver `connection refused` ×4, then `apiserver not ready` |
| The runner's own bug | acquisition p95 **266 s** | 335 sessions in flight against 150 offered |

So the honest statement is: **168 concurrent contexts is the slot ceiling on this node,
~2 sessions/s is what the control plane survives, and 600-in-10-minutes was a workload that
exceeded both.**

### The arithmetic, for a 24-hour run

Mean hold for `30s:40,2m:15,5m:5` is 75 s.

```
sessions/s = concurrent slots / mean hold
```

| Bound | Concurrency | sessions/s | sessions / 24 h |
|---|---|---|---|
| Slots | 168 | 2.24 | 193 500 |
| CPU, measured 2.0 core-seconds per session over ~2.7 free cores | 135 | 1.35 | 116 600 |
| Control plane (what actually broke) | — | ~2 | ~170 000 theoretical, **0 observed** |
| A defensible soak target | 75 | 1.0 | 86 400 |

## 3. Are we elastic, or hard stuck?

**Both, depending on the layer.**

| Layer | Elastic? | Why |
|---|---|---|
| Browser CPU | **Yes** — bursty by nature | no CPU limit (note 12), so a session that needs a core gets one while the node has it; a splash idles at ~1 m |
| Node capacity | **No** | fixed 6 cores; when requests run out the scheduler refuses, and there is no preemption victim to evict |
| Pool size | **Partially** | scales on claim count every 5 s, so it is demand-driven — but *blind to pressure*: a browser pinned at one core and one idling at 1 m look identical to it |
| Session admission | **Partially** | class queue + `--admit` bound; no CPU-aware admission |
| Slot allocation | **Yes** | slots are created on demand per session and disposed at release |

The gap the 600-session run exposed: **the pool grows because sessions are waiting, not because
the node is under pressure**. It scaled to 21 browsers while `Insufficient cpu` was being logged,
because the only signal it reads is the claim count. That is `experiments-wnj`.

## 4. kine: why the control plane is the real ceiling

k3s does not run etcd. It runs **kine**, a translation layer that presents the etcd API over a
SQL query log — here, SQLite in a single file. Every object the cluster stores becomes a row:
pods, leases, events, cilium endpoints, and our claims.

**Metrics exist.** The apiserver `/metrics` endpoint carries 376 kine series today:

```
kine_sql_time_seconds_bucket/count/sum     SQL latency, the number that matters
kine_sql_total                             statements by type
kine_compact_total{result="success"}       log compaction
```

**The evidence from the live cluster** (journal, while a run was on):

```
level=info msg="Slow SQL: INSERT INTO kine(…) SELECT '/registry/events/sandbox-demo/browser-pool-c2r86.…'  duration=1.54s
level=info msg="Slow SQL: INSERT INTO kine(…) SELECT '/registry/cilium.io/ciliumendpoints/sandbox-demo/browser-pool-c2r86'  duration=2.60s
level=info msg="Slow SQL: INSERT INTO kine(…) SELECT '/registry/leases/agent-sandbox-system/…'  duration=2.94s
```

Three different object kinds, 1.5–2.9 s per commit, on the same SQLite writer. Consequences that
look like platform failures but are one bottleneck:

- the sandbox controller loses its **lease** (`leader election lost`) and restarts → 119 restarts;
- the cilium operator churns (106 restarts) because endpoint writes queue behind the same writer;
- our `POST /sandboxclaims` gets **EOF** — the apiserver died mid-request, not our code;
- every session costs **writes**: claim create, sandbox status, events, claim delete, plus a
  cilium endpoint create/delete per pod.

**Per session, on this cluster: 5 writes minimum.** At 2 sessions/s that is 10 writes/s through one
SQLite file with fsync. That is the wall.

### Why SQLite: not a bug, a choice

k3s's default datastore is SQLite for single-node installs. It is one writer, one file, one
filesystem. Every `POST` is a transaction commit; kine's log grows and is compacted in background
jobs (`kine_compact_total`). Under mixed write load from controllers that all react to each other
(pod → cilium endpoint → event → lease → pod), latency compounds.

Options, in increasing cost:

| Option | Effect | Cost |
|---|---|---|
| Scrape kine metrics (`experiments-0fo`) | makes the bottleneck visible and attributable | a scrape job + RBAC |
| Cut writes per session (`experiments-9kl`) | fewer transactions: reuse claims, drop per-pod events, avoid status churn | runner + controller changes |
| Move k3s to external etcd | removes the single-file writer entirely | a second datastore, more memory, GPU host unaffected |
| Cap the offered rate (`experiments-c4h`) | stays inside what kine survives | nothing but discipline |

## 5. What to measure next, and in what order

1. **`experiments-0fo`** — scrape apiserver + kine metrics into VictoriaMetrics. Until this
   exists, every boundary reads as "connection refused" with no cause attached.
2. **`experiments-9b0`** — fix the dashboard (folder, missing series, per-session CPU/heap,
   pod memory over time, OOM/throttle counters). Reading the data is worth nothing if the panel
   queries the wrong metric.
3. **`experiments-ujf`** — fix the vocabulary in the report and in this note's §1, so "37
   sessions on an instance" stops reading as concurrency.
4. **`experiments-wnj`** — give the pool a pressure signal (CPU headroom), not just a queue count.
5. **`experiments-9kl`** — reduce writes per session, measured against `kine_sql_time_seconds` p99.
6. **`experiments-c4h`** — the ladder that turns all of this into a measured sessions/s vs
   resource table: 0.5, 1, 2, 4 sessions/s, ten minutes each, kine metrics live.
7. **`experiments-kvx`** — export pod memory as a gauge so the dashboard can show it over time.

## 6. One-line answers

- **600 sessions in 10 minutes is a workload, not a limit.** The node's slot ceiling is ~168
  contexts; the control plane's is ~2 sessions/s.
- **CPU is elastic per browser** (no limits, bursty) but **the node is not**, and the pool cannot
  see CPU at all.
- **kine/SQLite is the binding constraint**, with 1–3 s commits under load and a measurable
  histogram that we are not yet scraping.
- **Session ≠ slot ≠ pod ≠ claim.** A slot is one browser context; a session leases one slot for
  a hold; a pod carries N slots; a claim buys a pod.
