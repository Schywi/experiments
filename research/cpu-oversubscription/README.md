# CPU oversubscription — a study of what this cluster can actually pack

| | |
|---|---|
| Opened | 2026-10-31 |
| Status | Measurement added, experiments not yet run |
| Code | `apps/session-lab`, branch `research/session-lifecycle-elasticity` |
| Issues | `experiments-wnj` elasticity, `experiments-c4h` ladder, `experiments-0fo` kine metrics |
| Notes here | [01 — analysis of our implementation](01-analysis.md), [02 — kine and the MySQL question](02-kine-and-datastores.md), [03 — the Gemini design, reviewed](03-gemini-design-review.md) |

This folder exists to answer one question: **how many browser sessions can this node hold, and
what breaks first when we push it?** The swap study answered the memory side; this is the CPU side.

---

## The misunderstanding this study has to avoid

Two numbers get confused constantly:

| Number | What it is | Where it comes from |
|---|---|---|
| **`about:blank` / `example.com` session** | Chromium idling on an empty page | our default runs |
| **real workload session** | a page that parses, executes script and lays out | `test/` riot runs |

Gemini's research assumes a resting cost of **3–10 mCPU per session**. That matches an idle page.
Our measured share with a real page is **19–42 mCPU per session** — 4–8× higher — and a full
single-page app costs **~4 seconds of main-thread time** against **54 ms** for a splash image.

So: a benchmark on `about:blank` measures the *platform's* overhead. A benchmark on real pages
measures *what a browser actually costs*. Both are useful, they are not interchangeable, and every
report from now on must say which one it is. The riot test states it in the workload line.

---

## What was measured before this study started

| Finding | Value | Source |
|---|---|---|
| Browser request / limit | 100m / **none** | `config/sandbox/values.yaml` |
| Node | 6 cores, 15.5 GiB, 110 pods | `kubectl describe node fedora` |
| Sessions that fit by CPU requests | ~52 (6000 − 721 system) / 100 | arithmetic, verified against `describe` |
| Sessions that fit by memory requests | ~54 (15550 − 1600) / 256 | same |
| Actual per-pod memory, 8 contexts | ~360 MiB | `20261031T024806Z` |
| Per-session CPU, idle page | 1.26 core-s over 30 s | `20261031T010911Z` |
| Per-session CPU, real pages | 4.67 core-s | riot run |
| Throttled sessions | 248/360 with a limit, **0** without | notes 12, 18 |
| Control plane | ~2 sessions/s before kine refuses | journal + `20261031T024806Z` |

**Consequence:** lowering the CPU request from 100m to 50m cannot increase density much — memory
requests and the 110-pod cap bind at almost the same point. The test is worth running because the
*answer* is the deliverable, not the gain.

---

## What this study needs from the code (added in this change)

| Addition | Why |
|---|---|
| `--boot-concurrency N` | bounds simultaneous instance startups: the only mechanism that prevents overlapping boot spikes, which is the failure mode the Gemini paper describes (our equivalent of a token bucket, without a new dependency) |
| `session_lab_instances_booting` gauge | makes boot overlap visible while it happens |
| `session_lab_boot_seconds` histogram | how long a boot takes under contention |
| `session_lab_node_cpu_cores` gauge | whole-node CPU each second, because a pod's cgroup cannot be read before it is Ready |
| `node cpu` line in the report | peak and mean cores per run, so a report states whether it saturated the node |
| `RIOT_HEAVY` knob | raise the fraction of heavy pages to create deliberate spike convergence |

The gap these close: before this, **no measurement existed between "claim created" and "browser
Ready"**, so overlapping startup spikes could only be inferred from latency.

---

## The experiment ladder

```bash
cd apps/session-lab

# 1. baseline: real pages, 30 concurrent, current 100m request
SESSION_LAB_INTEGRATION=1 RIOT_SESSIONS=300 RIOT_HOLD=30s RIOT_RATE=1 \
  go test -tags integration -count=1 -timeout 1800s ./test/ -run TestRiotLive -v

# 2. same workload, boot concurrency capped at 4 (no config change needed)
SESSION_LAB_INTEGRATION=1 RIOT_SESSIONS=300 RIOT_HOLD=30s RIOT_RATE=1 --boot-concurrency 4 …

# 3. spike convergence: 10 of 30 sessions load the SPA together
SESSION_LAB_INTEGRATION=1 RIOT_SESSIONS=30 RIOT_HOLD=30s RIOT_RATE=2 RIOT_HEAVY=10 …
```

Step 2 needs the flag passed to the runner; the riot test builds `model.RunConfig` directly, so
`RIOT_BOOT` is the knob (wired with the same env helper) — see `test/riot_live_test.go`.

## Stopping conditions

Derived from the baseline, not invented:

| Signal | Stop when |
|---|---|
| `session_lab_node_cpu_cores` | peak > 0.9 × total cores sustained for > 10 s |
| `session_lab_instances_booting` | exceeds 4 with `node cpu` above 80 % |
| p95 acquisition | 2× the baseline step |
| `session_lab_boundaries_total{kind="FailedScheduling"}` | any |
| session failures | any not explained by a control-plane error |
| `kine_sql_time_seconds` p99 | > 1 s — the run then measures kine, not the platform |
