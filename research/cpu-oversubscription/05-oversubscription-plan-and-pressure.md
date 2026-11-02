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

## 3. Three gaps, verified

### Gap 1 — the autoscaler is blind to CPU (confirmed)

`autoscale.go:89` reads claim counts and nothing else. No CPU, no PSI, no `FailedScheduling`.
Consequence: it grows the pool toward demand with no idea whether the node has headroom, which is how
`20261031T024806Z` reached 21 browsers and then `FailedScheduling … Insufficient cpu`.

### Gap 2 — the class label is never written (confirmed, and it is a bug)

- `labelClass = "session-lab.io/class"` — `kube.go:46`
- read by the demand watcher — `watch.go:33`
- **written nowhere**: `Acquire` builds `"labels": {managedBy: managedValue}` only — `kube.go:143`

So `ClaimStats.Bulk` is always 0, `--class bulk` reorders the local queue and nothing else, and the
autoscaler's "bulk never grows the pool" rule is dead code. One line fixes it: add
`labelClass: string(class)` next to `managedBy` in `Acquire`.

### Gap 3 — the boot spike is measured, then ignored (confirmed)

`instance.BootCPUPeakCores` is set in `boot.go:86` and read by nobody. k_max is typed into
`--boot-concurrency`. It could be derived at run start: `k_max = (cores × 0.8) / measured_peak`.

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
