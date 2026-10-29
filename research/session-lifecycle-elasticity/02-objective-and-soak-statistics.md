# Note 02 — What the run is for, and what a 10k-session soak can claim

| | |
|---|---|
| Status | Draft for review |
| Date | 2026-10-29 |
| Phase | 8 — session lifecycle elasticity |
| Scope | Choosing the experiment shape before writing any code, and the statistics that decide what a result may say |
| Evidence base | agent-sandbox checkout `d816e0f`; see the folder [README](README.md) for provenance |
| Related | [01 — CPU and memory instrumentation](01-instrumentation-cpu-memory.md), [03 — what exists today](03-what-exists-today.md), [04 — the deliverable](04-deliverable.md) |

---

## Status of the numbers in this note

§3 and §7 are arithmetic and statistics on operator-chosen inputs. Two places quote a figure from
elsewhere, and neither is a measurement of this cluster:

| Quantity | Status |
|---|---|
| the concurrency-to-host conversion in §2 | divides by a browser-per-host density taken from the fork's research; unverified, and note 04 §2 keeps it out of the runner's inputs |
| the `120 ms → 480 ms` line in §3 | an illustration of a claim shape, not a result |

---

## 1. Three questions that need three different runs

The scenario "10k sessions over 20 hours, heterogeneous length, exclusive hold, elastic pool" mixes
three experiments. Each has its own shape and its own existing tool, and only the first can be
asked of a long session stream.

| # | Experiment | Question | Shape | Tool already in the fork |
|---|---|---|---|---|
| A | Soak / correctness | Do rare, compound failures exist over time? | many sessions, modest concurrency, long wall clock, outcome taxonomy | `test/stress` `claims-warm-sustained` |
| B | Capacity | Where does the system stop converging? | ramp until non-convergence, short steps | `dev/load-test/test-recipes/` capacity-cliff (CL2) |
| C | Mechanism / A-B | Does this lever change the outcome? | paired runs, one variable | `test/stress` phases + `--label:` |

The objective of A is a failure-class taxonomy with a denominator at a concurrency the box can
hold. B produces a number pair: the limit, and the constraint that binds. C turns an observation
into a finding.

That split is already the ground rule in the fork's experiment index (`research/experiments/README.md`:
write the Expected column before running) and is executed at
`research/experiments/02-capacity-6vcpu-100k.md:146-172`, where the Little's-law table does the
work of turning a target rate into a host count.

## 2. The arithmetic of 10,000 sessions over 20 hours

10,000 sessions / 72,000 s = **0.139 sessions/s**. Little's law over an equal mix of the four
length buckets, mean 262.5 s:

| Bucket | Concurrency |
|---|---|
| 30 s | 4.2 |
| 2 min | 16.7 |
| 5 min | 41.7 |
| 10 min | 83.3 |
| mixed mean | ~37 |
| mixed peak (×3) | ~110 |

At the fork's measured planning density of ~9 active Chromium per 6-vCPU host
(`research/experiments/02-capacity-6vcpu-100k.md:128-144`), ~110 concurrent peak is ~12 hosts.
On one box the same session count over 20 hours becomes a queueing experiment, and the queue is
the finding.

## 3. What a failure count can claim

With `n` sessions and `X` observed failures:

- `X = 0` → 95% upper bound ≈ `3/n` = 3×10⁻⁴ (rule of three). The claim is "≤300 ppm", never
  "zero".
- 95% CI ≈ `(X ± 1.96·√X)/n`. `X = 10` at `n = 10⁴` gives 3.8×10⁻⁴ … 1.6×10⁻³ (±60% relative).
  A ±20% interval needs ~100 failures.
- Resolution: a true rate of 10⁻³ shows up (~10 events); 10⁻⁴ probably does not (~1 event). 10k
  sessions resolves rare classes, and rare rates only coarsely.

Assertable after a 20-hour run:

- "In 10,000 sessions over 20 h we observed ≥1 event of classes {claim stuck after pool dry,
  adoption 409 storm, expiry-before-Ready, router 502 on recycled UID}, at ≤3×10⁻⁴/session each."
- "The 10-minute bucket's p99 claim→Ready moved from 120 ms at hour 2 to 480 ms at hour 18."

Not assertable from the same run:

- "The system handles 10k sessions" — no concurrency denominator.
- "Failure rate is 0.3%" — 10k sessions does not resolve that precision unless `X ≈ 100`.

What the 20 hours buy that no ramp does: reconciliation churn over long horizons (cache and
expectation drift, lease churn, leader changes), refill starvation after many pool cycles, expiry
races at the 30 s and 10 min boundaries, and resource creep (fd, socket, PVC, RSS) — the last one
needs an hourly slope, not a session metric.

## 4. What tracing sees of this scenario

The distinctions in the scenario are API-level, so kernel tracing cannot see them:

| Concept | Where it lives | Visible to tracing |
|---|---|---|
| warm adoption vs cold start | controller label `launch_type` (`internal/metrics/metrics.go:60-140`) | no — both are a pod starting |
| claim waiting because the pool is dry | `warmCandidatesPendingError` (`extensions/controllers/sandboxclaim_controller.go:88-95`), requeue at `:362` and `:466-490` | no syscall corresponds to it |
| agent 1 holds instance B | no lock exists; `SandboxClaim`↔`Sandbox` is 1:1 and permanent (`extensions/api/v1beta1/sandboxwarmpool_types.go:55-61`) | partially — see §1 of note 01 |
| session expiry at 30 s / 10 min | `spec.shutdownTime` → `expired` label (`api/v1beta1/sandbox_types.go:314-347`) | no |
| the pool grew | HPA status | no |

Signals that are load-bearing, and where to get each without a BPF program:

| Question | Best source | Tracing needed |
|---|---|---|
| Is the "held" instance idle-waiting or CPU-busy? | cgroup v2 `cpu.stat`, `cpu.pressure` per pod | no |
| Did idle→active swap-in stall the session? | `memory.pressure`, `memory.events`, swap counters | no |
| Is the session blocked on disk or network? | `io.pressure`, TCP retransmit and connect counters | yes for sub-session attribution |
| Did the data plane refuse, drop or delay? | Hubble flow verdicts (`research/05-cilium-networking.md:169`) plus cilium-agent metrics (`test/stress/promscrape.go:158-165`) | no — already eBPF-based |
| What does a fresh pod cost against a "blank" reuse? | `sched:sched_process_exec` counts, `sched_stat_runtime` | yes |
| Which container died, and why | `oom:mark_victim`, kubelet events | optional |

The single change with the widest effect for the least code is replacing the 0.3 vCPU assumption
with measured per-session CPU — the gap that note 01 closes. Per-connection and per-syscall data
must not become a Prometheus label (`AGENTS.md` forbids unbounded cardinality); the harness writes
JSONL and joins offline.

## 5. Control-plane metrics: what exists, what is missing

Present in `internal/metrics/metrics.go`:

| Metric | Labels | Use |
|---|---|---|
| `agent_sandbox_claim_startup_latency_ms` | `launch_type`, `sandbox_template` (`:60-70`) | the SLO, split warm/cold |
| `agent_sandbox_creation_latency_ms` | `namespace`, `launch_type`, `sandbox_template` (`:122-129`) | pod-level start |
| `agent_sandbox_claim_creation_total` | `namespace`, `sandbox_template`, `launch_type`, `warmpool_name`, `pod_condition`, `created_by` (`:131-149`) | denominators, warm/cold ratio |
| `agent_sandboxes` | `ready_condition`, `expired`, `launch_type`, `sandbox_template`, `owned_by`, `created_by` (`:151-167`) | inventory, expiry |
| `agent_sandbox_warmpool_size` | `warmpool_name`, `sandbox_status` ∈ ready/pending/succeeded/failed (`:169-180`) | pool fill state |

Missing, and each is a hole in the scenario:

1. **Waiting claims / queue depth.** Nothing reports "N agents are waiting for a pool"; it can
   only be inferred from requeue logs. Needed: a gauge per pool, plus `pool_dry_seconds_total`
   (time with `readyReplicas == 0` while claims are pending).
2. **Session length as a label.** Nothing slices the metrics above by 30 s / 2 min / 5 min /
   10 min, because dwell is a harness flag (`--claim-dwell`, `test/stress/main.go:182-185`) and
   not an API field.
3. **Outcome taxonomy.** `SandboxRecord.Error` is free text (`test/stress/tracker.go:151`) — not
   countable, not chartable.
4. **Reuse accounting.** No `launch_type: reuse`, no per-sandbox session counter, because the
   primitive does not exist (§5 of note 03).
5. **Scale-out lead time.** Nothing joins HPA replica transitions to `readyReplicas` reaching
   target.

## 6. The zero-code fix for per-bucket slicing

Run one `SandboxTemplate` per length bucket — `chrome-s30`, `chrome-s2m`, `chrome-s5m`,
`chrome-s10m` — with one pool each, and dispatch each claim to the matching pool.
`sandbox_template` is already a label on every latency histogram and on
`agent_sandbox_claim_creation_total`, so per-bucket SLOs, warm ratios and failure counts exist
without a code change. It also produces the pool shape that
`research/experiments/02-capacity-6vcpu-100k.md:190` recommends ("pool per tenant tier, not per
tenant — pools cost idle browsers"). The cost is four pools of idle pods, which is itself a
finding about the idle-cost tradeoff.

## 7. Instrumentation plan: joins, not new plumbing

One row per session, three sources joined on one key. The harness already emits the join keys:
`PodUID`, `NodeName`, `ContainerID`, `BoundSandbox` at `test/stress/tracker.go:108-122`, Prometheus
samples long-and-narrow for DuckDB (`test/stress/promscrape.go:44-52`), and the watch streams in
`watch.jsonl.gz` for offline ground truth.

1. **Plan side (the denominator).** Write the intended session table before running — four buckets
   × arrival schedule × 20 h, with exact counts. Every report line divides by planned, so a lost
   session shows up as a loss.
2. **Session row.** `{session_id, bucket, template, pool, arrival_ts, claim_created, ready_ts
   (warm|cold), hold_start, hold_end, delete_ts, gone_ts, node, pod_uid, cpu_core_seconds,
   cpu_pressure_integral, swap_in_bytes, oom, outcome_enum}` with the CPU and memory integrals
   from note 01 §6.
3. **Outcome enum** (normalizes `Error`): `ok`, `fail_pool_dry_coldstart`, `fail_queue_timeout`,
   `fail_schedule`, `fail_image_pull`, `fail_ready_timeout`, `fail_expired_before_ready`,
   `fail_oom`, `fail_router_502`, `fail_delete_stuck`, `lost_unknown` — ten fixed values, countable
   and safe as a low-cardinality label.
4. **Sampling economy.** Reading the per-pod cgroup at 1 Hz for 10k sessions × 20 h is waste. Four
   boundaries (after create, first Ready, hold midpoint, before delete) plus a 5 s series only for
   the 5-minute and 10-minute buckets.
5. **Invariants as assertions.** `sessions_planned == sessions_ok + Σ fail_*`; mean concurrency ≈
   `λ·W` (Little's law as a test); `expired_count == scheduled_expiries`; `warm_ratio +
   cold_ratio == 1`. A run whose Little's law does not close has a measurement defect, and no
   other number from it is quotable.
6. **Three report slices, never blended:** by bucket, by `launch_type`, by hour. The hourly slice
   is what the 20 hours are for.

## 8. How to check this note

```bash
cd ~/Documentos/git_repos/agent-sandbox

# the metrics this note claims exist, and their labels
rg -n "agent_sandbox_(claim_startup_latency_ms|creation_latency_ms|claim_creation_total|warmpool_size)" \
  internal/metrics/metrics.go

# dwell is one scalar flag, not an API field
rg -n "claim-dwell" test/stress/main.go

# free-text error field that cannot be counted
rg -n "Error string" test/stress/tracker.go

# the Little's-law table the arithmetic follows
rg -n "Session duration|avg concurrent" research/experiments/02-capacity-6vcpu-100k.md
```
