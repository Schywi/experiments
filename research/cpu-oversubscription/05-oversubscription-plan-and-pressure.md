---
title: '05 — Oversubscription plan, pressure, and the three real gaps'
kind: design
state: unknown
commits: [77cc684, c757981, 8caec1c, 3a4069e]
tags: [cpu]
---

# 05 — Oversubscription plan, pressure, and the three real gaps

Verified against the code on this branch, 2026-10-31. Line numbers are current.

## 1. The three-step plan (flags and config only)

| Step | Change | Where |
|---|---|---|
| 1 | browser CPU request `50m` → `10m`, no limit | `config/sandbox/values.yaml:91` |
| 2 | autoscaler cap `--max 40` → `--max 120` | `cmd/pool-autoscaler/main.go:43`, or `config/session-lab/values.yaml` |
| 3 | cap concurrent startups at k_max (10–16) | `--boot-concurrency N` on the runner; published to the pool as `session-lab.io/boot-concurrency` (`kube.go:624`, read at `:501`) |

Why 10m is safe to *schedule*: a request is admission, not enforcement, and there is no CPU limit.
Why it is not free: the browser still needs ~1.9 cores to cold-start and ~0.1 core to idle, so the
scheduler will admit far more pods than the node can run at once. Step 3 is the counterweight.

## 2. What "pressure" means

Pressure is **time lost waiting**, not how busy a resource is.

`/proc/pressure/cpu`:

| Field | Meaning |
|---|---|
| `some avg10` | share of the last 10 s in which something was runnable but had no core |
| `full avg10` | share of time in which *everything* stalled |
| `total` | cumulative stall microseconds |

Busy ≠ pressured: 90 % CPU with `full avg10 = 0` is healthy; 60 % CPU with `full avg10 = 30` is
thrashing. `cpu.stat throttled_usec` measures a *different* thing — quota starvation, and it is
always 0 here because there is no CPU limit.

### Pressure-based sizing

```
every 5s:
    q    := ready + pending claims            # exists today (autoscale.go:89)
    p    := node cpu.pressure.full avg10      # NEW
    util := cores_used / cores_allocatable    # sampled already, not exported to the autoscaler

    if p > 25 or util > 0.85:      desire := desired - 1        # saturated: relieve
    elif p < 5 and util < 0.70:    desire := ceil(q / contexts_per_browser)
    else:                          desire := desired           # hysteresis band

    desire := clamp(desire, min, max)          # plus the boot cap below
    patch replicas only if desire != desired
```

Two properties make it safe: a **hysteresis band** (act only outside it, or it oscillates at 5 s) and
**asymmetry** (grow when free, shrink when pressured). Missing pieces: node PSI is not read at all
(pods are), and the autoscaler has no second input.

## 3. Three gaps, verified — and since fixed

All three were confirmed against the code below and have since been implemented. Status and
locations are given so the section reads as a record, not an open list.

### Gap 1 — the autoscaler is blind to CPU (fixed, `77cc684`)

`autoscale.go` read claim counts and nothing else, so it grew the pool toward demand with no idea
whether the node had headroom — how `20261031T024806Z` reached 21 browsers and then
`FailedScheduling … Insufficient cpu`. It now takes a second input: node CPU PSI `full avg10` and
interval CPU utilisation (`internal/infra/measure/node.go`), wired through `Config.NodeLoad`
(`autoscale.go:64`). Each step shrinks one replica when saturated, grows toward demand when free, and
holds inside a hysteresis band (`autoscale.go:157`).

### Gap 2 — the class label is never written (fixed, `c757981`)

`Acquire` built `"labels": {managedBy: managedValue}` only, while the demand watcher already read
`session-lab.io/class`. `ClaimStats.Bulk` was therefore always 0 and the "bulk never grows the pool"
rule was dead code. `Acquire` now writes `labelClass: string(class)` next to `managedBy`
(`kube.go:143`), and `TestObserveClaimReadsClassLabel` covers the round trip.

### Gap 3 — the boot spike is measured, then ignored (fixed, `8caec1c` + `3a4069e`)

`instance.BootCPUPeakCores` was set and then discarded; k_max was typed into `--boot-concurrency`.
It is now persisted per instance and read back at preflight, so k_max is derived —
`k_max = (cores × 0.8) / measured_peak`. §6 works through the mechanism.

## 4. The line to draw

| Goal | Work |
|---|---|
| want the density | flags only, zero code |
| want it safe unattended | autoscaler reads pressure (gap 1) |
| want it routed | class → pool (gap 2) |
| want it automatic | k_max from the measured boot spike (gap 3) |

**Order: the flags-only ladder first.** If rungs hold, gap 1 is the one patch worth writing — without
it the autoscaler is the thing that converts good density into a node-wide stall.

## 5. What the ladder looks like, with the plan applied

```bash
cd apps/session-lab
SESSION_LAB_INTEGRATION=1 RIOT_SESSIONS=600 RIOT_HOLD=75s RIOT_RATE=2 RIOT_HEAVY=0 RIOT_BOOT=12 \
  go test -tags integration -count=1 -timeout 2400s ./test/ -run TestRiotLive -v
```

Read in the report: `node cpu peak X / 6 cores` (target 4.8–5.4), `first boundary:` (which wall),
`outcomes` (control-plane refusals vs OOM).

## 6. Deriving k_max from stored boot peaks (gap 3, worked through)

`--boot-concurrency` is the number of browsers allowed to cold-start at the same time. Choosing it
by arithmetic is possible:

```
k_max = (cores × 0.8) / measured_peak

cores          physical cores on the node            6 here
measured_peak  CPU cores one browser uses to start   1.5, 1.89, 1.74 measured
→ (6 × 0.8) / 1.89 ≈ 2.5  → allow 2 concurrent boots, node stays near 80 %
```

### The circularity

`measured_peak` is produced **during** a run, by sampling a booting pod's cgroup. A run that wants
`k_max` at its own start has nothing to read on the first attempt. That is the whole obstacle: the
formula needs a number that only exists after the thing it is sizing has happened.

### Breaking it: persist, then read back

```
run 1   peak unknown        → --boot-concurrency 2 (typed in, a guess)
        during the run      → 1.89 cores observed on instance 3
        end of run          → the value is written to the database      ← already implemented
run 2   at preflight        → read the stored peaks, take the highest
        k_max = (6 × 0.8) / 1.89 = 2       → --boot-concurrency 2, derived
```

Both halves exist:

| Piece | Where |
|---|---|
| columns `boot_cpu_peak_cores`, `class`, `contexts_per_browser` | `internal/platform/store/store.go:103-105` |
| written per instance | `store.go:355`, fed from `instance.BootCPUPeakCores`, `Class`, `ContextsPerBrowser` |
| measured at 100 ms from sandbox binding to Ready | `internal/runtime/session/boot.go` |
| `store.BootPeakCores(class, contexts)` — `MAX(boot_cpu_peak_cores)`, filtered | `store.go:474` |
| `store.PriorBootPeakCores(dir, runID, class, contexts)` — worst peak across prior runs | `store.go:493` |
| `deriveBootConcurrency(cores, peak)` — `int(cores*0.8/peak)`, floor 1 | `boot.go:101` |
| preflight branch when `--boot-concurrency` is 0 | `internal/runtime/session/run.go:296` |

One detail the "run 2 reads run 1" picture hides: each run writes its own `<run-id>.db`, so the read
cannot be a single query against the current store — its instances table is empty at preflight.
Preflight combines `BootPeakCores` (rows already in this run) with `PriorBootPeakCores`, which globs
the run databases in the output directory and takes the highest matching peak. A run with a fresh
`--out` directory, or the first run in one, has nothing to read and keeps the typed flag.

### The guard that keeps it honest

A boot peak depends on the workload being rendered and on `contexts-per-browser`, so each row carries
both and `BootPeakCores` filters on both. Deriving `k_max` from rows produced by a different workload
would let the number drift toward whatever the last experiment happened to be. No matching rows means
no derivation: the flag stands.

---

## Where this sits

- Collection: [cpu-oversubscription](README.md)
