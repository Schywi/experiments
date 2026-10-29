# Note 07 — The overload run and the suspend run: where the platform stops

| | |
|---|---|
| Status | Recorded |
| Date | 2026-10-29 |
| Phase | 8 — session lifecycle elasticity |
| Runs | overload `20261029T161101Z`, suspend `20261029T162748Z` (reports under `docs/session-lab-runs/`, gitignored) |
| Code | `apps/session-lab`, commits `159ab5b` … this one |
| Related | [04 — the deliverable](04-deliverable.md), [06 — audit](06-audit.md) |

---

## 1. The overload run

Sixty sessions, three length buckets, arrival rate 0.8/s (offered concurrency 60), pool set to
20 replicas, policy `reuse`:

```bash
session-lab --sessions 60 --mix 30s:40,2m:15,5m:5 --rate 0.8 \
            --policy reuse --pool-replicas 20 --namespace sandbox-demo --pool browser-pool
```

| Quantity | Measured |
|---|---|
| planned / stored | 60 / 60 |
| outcomes | `success` 42, `acquisition_failed` 17, `reset_failed` 1 |
| offered concurrency | 60.0 |
| measured concurrency | peak 18, mean 9.3 |
| queue wait (claim → Ready) | p50 3.2 s, p95 134 s, p99 136 s, max 136 s |
| hold | p50 30 s, p95 300 s |
| reset | p50 35 ms, p95 625 ms |
| CPU | 93.96 core-seconds total, 1.77 p50, 3.88 max |
| memory | 154 MiB p50 peak, 209 MiB max, 168 MiB working-set max |
| throttled sessions | 43 of 43 sampled |
| sessions with refaults | 40 of 43 |
| instances | 55, max 2 sessions per instance |
| node memory | MemAvailable floor 3322 MiB of 15924 MiB (20.9%), swap used 4418 MiB, 195 samples |
| template | requests 100m / 256MiB, limits 1000m / 768MiB |

## 2. What stopped it

Two constraints, in this order.

**The scheduler refused first, on CPU requests.** Seventeen acquisitions failed with
`0/1 nodes are available: 1 Insufficient cpu`, and those claims never reached Ready. At 100m of
CPU requests per sandbox pod, the node's 6000m allocatable admits roughly sixty pods before the
scheduler says no, and the run offered exactly that offered load, so the refusal arrived while the
pool was still refilling. These are the 17 `acquisition_failed` rows, each carrying the scheduler's
message.

**The instrument timed out once under the same load.** One session failed with
`reset: 502 Bad Gateway: {"error": "TimeoutError: "}` from the in-pod CDP bridge — the reset call
that a reused instance answers in 35 ms at low load took longer than the bridge's command budget
when eighteen browsers were live. That was the first boundary the report recorded, ahead of the
scheduler refusals, because it happened earlier in wall-clock terms.

**The node came close behind.** `MemAvailable` fell to 20.9% of total and swap carried 4.4 GiB, with
195 host samples across the run. Memory did not cause a failure in this run; it is the next wall,
and it is measured rather than assumed.

## 3. What the run says about capacity — and what it does not

- The sessions that did run were throttled: 43 of 43 sampled, at the 1000m CPU limit the template
  applies. Hold duration is therefore not a clean workload measurement at this concurrency.
- Measured peak concurrency was 18 against an offered 60. The difference is queueing, and it is
  visible in the queue wait: p50 3.2 s but p95 134 s.
- This run does not establish a capacity number for the platform. It establishes the constraint
  that binds first at this offered load on this node: CPU requests, then the browser bridge's
  command budget, with node memory approaching.
- The elasticity column reads `exceeded: see first_boundary`. Nothing scaled: no autoscaler exists,
  and the pool was fixed at what the operator asked for.

## 4. The suspend run

Ten sessions of 20 s, offered concurrency 5, pool 2, policy `suspend`:

```bash
session-lab --sessions 10 --mix 20s:10 --rate 0.25 \
            --policy suspend --pool-replicas 2 --namespace sandbox-demo --pool browser-pool
```

| Quantity | Measured |
|---|---|
| outcomes | `success` 10 |
| offered / measured concurrency | 5.0 / peak 5, mean 2.8 |
| queue wait | p50 107 ms, max 1207 ms |
| **resume** | **p50 2853 ms, p95 2859 ms, max 2918 ms** |
| hold | p50 20 017 ms |
| CPU | 15.22 core-seconds total, 1.44 p50, 1.90 max |
| memory | 140 MiB p50 peak, 140 MiB working-set max |
| instances | 5, two sessions each |
| throttled | 10 of 10 |
| node memory | floor 7675 MiB of 15924 MiB (48.2%), swap 3630 MiB |

Five resume cycles ran, one per reused instance, all successful: an instance is suspended after its
session, the next session resumes it, and the runner waits for Ready before holding.

Resume costs about **2.9 seconds** against **107 ms** for warm adoption on the same cluster, so a
suspended instance is roughly 27 times slower to hand to the next session than a warm one — and
about 35 ms cheaper than creating one, which this run did not measure and the rebuild column of a
`destroy` run would.

What survives a suspend is narrower than it looks: the Sandbox object, its Service and its volumes
survive, and the pod does not. The in-guest browser is gone, which is why there is nothing in this
run about page state — the reset path is what a reuse session pays, and a suspend session pays a pod
start instead.

## 5. Two defects these runs exposed

1. **A resumed instance was used before it was ready.** `session()` resumed a suspended instance and
   went straight into the hold; the resume recreates the pod, so the next session could have run
   against a sandbox that was not up. The runner now waits for Ready after a resume and records the
   timestamps it got. Without this, the suspend run's two reuses would have been luck.
2. **The replica trajectory was written but never read.** `EndRun` stored the samples in
   `runs.replicas`, and `report.Build` never loaded them, so every report carried an empty
   `replicas` array. `store.RunReplicas` now fills it.
3. **Resume had no number of its own.** The first suspend run folded resume into the next session's
   acquire time, so the report could not answer the question the policy exists to ask. The runner
   now times the resume (patch to Ready) per session, stores it, and exports it as
   `session_lab_resume_seconds`.

## 6. What to run next

1. **Repeat at offered 40** to check the boundary is repeatable rather than a one-off. If 40 also
   refuses, the constraint is the node's CPU requests and no amount of pool sizing moves it.
2. **Raise the CPU limit on the template and repeat** at the same offered load. Every session in
   both runs was throttled, so the hold numbers carry the limit. The template is GitOps-owned
   (`config/sandbox/values.yaml`), so this is a reviewed change, not a runner flag.
3. **Give the reset path its own budget.** One 502 at 18 concurrent browsers says the bridge's
   command timeout is tuned for a quiet cluster. Either raise it or record it as a boundary that the
   experiment must stay under.
4. **Compare resume against rebuild.** A `destroy` run at the same offered load would give the
   number this note is missing: 2.9 s of resume against the cost of deleting and cold-starting.

## 7. How to check this note

```bash
sqlite3 docs/session-lab-runs/20261029T161101Z.db \
  "select outcome, count(*) from sessions group by outcome;"
sqlite3 docs/session-lab-runs/20261029T161101Z.db \
  "select count(*) from sessions where throttled_usec > 0;"
kubectl -n sandbox-demo get events --field-selector reason=FailedScheduling
```
