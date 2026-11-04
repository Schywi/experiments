---
title: '06 — What a lightweight riot run actually does'
date: 2026-11-02
kind: analysis
state: unknown
tags: [cpu, state-unknown]
---

# 06 — What a lightweight riot run actually does

The path a splash-only run (`RIOT_HEAVY=0`) takes through the code on this branch, step by step. This
is the reference for reading a run's log, and for knowing which step a boundary belongs to.

## The path

```
STEP 0  YOU RUN
        go test -tags integration ./test/ -run TestRiotLive
        env: RIOT_SESSIONS RIOT_HOLD RIOT_RATE RIOT_HEAVY(0 = splash only) RIOT_BOOT

STEP 1  test/riot_live_test.go
        ├─ riotURLsWithHeavy(n, heavy)   → splash URLs, distinct champion + skin each
        ├─ store.Open(docs/session-lab-runs/riot-<ts>.db)   WAL, one writer
        ├─ kube.New(...)                                     dynamic client
        ├─ schedule.Plan(cfg)                                arrival offsets
        └─ session.New(Runner){Adapter, Store, Plan, Sampler, BootConcurrency}

STEP 2  Runner.Run — preflight (blocking, ~1s)
        ├─ GetPool          → desired, ready, template=browser-tpl
        ├─ Node allocatable → cpu=6000m mem=15923Mi pods=110
        ├─ Template         → requests cpu=10m mem=128Mi, limits cpu=none mem=768Mi
        ├─ SetPoolPaused(false)      the run owns the pool
        ├─ SetBootConcurrency(N)     annotation session-lab.io/boot-concurrency
        └─ StartRun(row)             run row in SQLite

STEP 3  background work starts
        ├─ host sampler  every 1s: /proc/stat → node cores, /proc/meminfo → available
        ├─ metrics :9109/metrics
        └─ dispatcher    one timer per session, offset = i / rate
```

Per session, once its timer fires:

```
        ┌────────────────────────── one session goroutine ──────────────────────────┐
        │ 1  admission.Gate.Acquire(class)     --admit 0 → never blocks             │
        │ 2  boot permit (--boot-concurrency)  --boot 0 → never blocks              │
        │ 3  slots.Pool.Take()                                                      │
        │      ├─ free slot on a live browser? ────────────────► REUSE, no API call │
        │      └─ none → provision, ONE at a time                                   │
        │ 4      ├─ POST sandboxclaims {warmPoolRef: browser-pool}   ← kine write   │
        │        │    retry x5 on transport errors                                  │
        │        ├─ sampleBoot: ResolvePod → pod UID, then read that cgroup every   │
        │        │    100ms; peak ≈ 1.5–1.9 cores                                   │
        │        ├─ WaitReady: WATCH claim until Ready                              │
        │        │    warm adoption ~250ms · cold start 12–80s                      │
        │        └─ persistInstance(boot_cpu_peak_cores)                            │
        │ 5  openIsolated  ws → podIP:9224                                          │
        │      ├─ GET /json/version (discovery)                                     │
        │      ├─ Target.createBrowserContext  → one context per session            │
        │      ├─ Target.createTarget(url)     → one tab in that context            │
        │      └─ EnablePage + Performance.getMetrics                               │
        │ 6  HOLD d = RIOT_HOLD                                                     │
        │      every --sample-seconds: cpu.stat · memory.current/stat/events ·       │
        │      cpu.pressure, plus boundary detection                                │
        │                                        splash ≈ 45 MiB per context        │
        │ 7  end of hold                                                            │
        │      ├─ Performance.getMetrics → task ms, script ms, JS heap              │
        │      ├─ close target, dispose context                                     │
        │      ├─ divideReading by contexts in flight → per-session share           │
        │      └─ slots.Give(instance, slot)                                        │
        │ 8  reuse: browser stays, claim stays, slot returns to the pool            │
        │      destroy → DELETE claim · suspend → operatingMode: Suspended          │
        │ 9  settle(outcome, timings) → INSERT session row + recorder counters      │
        └───────────────────────────────────────────────────────────────────────────┘
```

Teardown:

```
STEP 4  end of run
        ├─ restorePool        replicas back, annotation cleared
        ├─ delete held instances (reuse keeps them until here)
        ├─ report.Build → Write(<run>.json) → Print(stdout)
        │     outcomes · queue wait · acquire · hold · reset · cpu/mem ·
        │     node cpu peak X / 6 · throttled · oom · first boundary
        ├─ assertNoRiotLeaks  no lab- claims left
        └─ t.Cleanup          rewrite the report if it was not written
```

## Which boundary belongs to which step

| Step | Boundary | Log signature |
|---|---|---|
| 4 | datastore stall → API server gone | `acquisition_failed … connection refused` |
| 4 | pool dry, cold starts | `ready (cold)`, `boot cpu peak_cores=1.5–1.9` |
| 6 | memory limit 768Mi (SPAs only) | `lost_unknown` + `oom_kill` in the row |
| 6 | CPU saturation | `node cpu peak` → 6/6, hold p95 climbs |
| 2/4 | scheduler | `boundary: FailedScheduling … Insufficient cpu` |

## Splash versus SPA, on the same path

| | splash | SPA |
|---|---|---|
| memory per context | ~45 MiB | ~96 MiB |
| task time | 50–100 ms | 2 000–4 000 ms |
| boot CPU peak | 1.5–1.9 cores | 1.5–1.9 cores |
| first wall | **kine**, above ~2 arrivals/s | the 768Mi limit, at 8 contexts |

A splash stress run above ~2 arrivals/s therefore fails in step 4, at the claim POST, and the report
says so in `first boundary`. The browser path is never reached.

## Current platform settings these steps read

| Setting | Value | Where |
|---|---|---|
| browser CPU request | **10m** | `config/sandbox/values.yaml:91` |
| browser memory request / limit | 128Mi / 768Mi | same block |
| autoscaler range | 2 .. **120** | `config/session-lab/values.yaml:36-37` |
| boot concurrency | flag; published as an annotation | `--boot-concurrency`, `kube.go:624` |

---

## Where this sits

- Collection: [cpu-oversubscription](README.md)
