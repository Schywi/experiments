# Handoff — session-lab, for the CPU-oversubscription investigation

| | |
|---|---|
| Written | 2026-10-31 |
| Branch | `research/session-lifecycle-elasticity` at `30df98e` |
| Reader | the agent picking up **CPU oversubscription / density** |
| Prior art | swap/zram density was validated separately under `research/swap-capacity/` and `config/vm-swap-lab/`; this note is the CPU side |

Everything below is measured on this cluster, not estimated. Where a number comes from a run,
the run id is given; the artefacts live in `docs/session-lab-runs/` (gitignored).

---

## 1. What exists

`apps/session-lab` is a Go experiment runner that drives real Chromium sandboxes through the
`agent-sandbox` API. It is not a load generator bolted on: it schedules sessions, holds one
browser context exclusively per session, measures the resources each one consumes, and records
where the platform refuses.

```
claim (capacity request) → pod (one Chromium, one cgroup) → slot (one browser context) → session (one lease)
```

- **Two layers.** `--layer infra` = one session per pod (claims/scheduling). `--layer session` =
  up to 8 sessions per Chromium via `Target.createBrowserContext` (the session mechanic).
- **Transports.** `--cdp-transport http` goes through the in-pod bridge; `ws` speaks CDP directly
  over a Go sidecar (`apps/cdp-forward`) on pod IP `:9224`.
- **Measurement.** Per session, from cgroup v2: `cpu.stat` (`usage_usec`, `nr_throttled`),
  `memory.current/stat/events`, PSI. Per context, from CDP `Performance.getMetrics`:
  `task_duration_us`, `script_duration_us`, `js_heap_bytes`.
- **Autoscaler.** `cmd/pool-autoscaler` sizes `SandboxWarmPool.spec.replicas` from claim demand,
  divided by `--contexts-per-browser`; it honours a `session-lab.io/autoscaler=disabled`
  annotation that the runner sets for fixed-pool runs.
- **Record.** SQLite per run, plus a JSON report with percentiles, invariants and a `next:` hint.

### Commands

```bash
cd apps/session-lab
go test ./...                                  # unit, no cluster (14 packages)

# a scripted run
scripts/session-lab-experiment.sh --layer session --duration 300s --rate 0.5 --hold 30s

# real pages, one heavy League SPA + a distinct splash per session
SESSION_LAB_INTEGRATION=1 RIOT_SESSIONS=300 RIOT_HOLD=75s RIOT_RATE=0.5 \
  go test -tags integration -count=1 -timeout 1800s ./test/ -run TestRiotLive -v
```

---

## 2. The CPU question, and what is already known

### 2.1 The configuration under test

| | value | source |
|---|---|---|
| Browser CPU request | **100m** per pod | `config/sandbox/values.yaml` |
| Browser CPU limit | **none** (policy, note 12) | same file, commented field |
| Browser memory | 256Mi request, 768Mi limit | same |
| Node | 6 cores / 16 GiB, k3s single node | `kubectl describe node fedora` |
| Slots per pod | 1 (infra) or 8 (session) | `--contexts-per-browser` |

The limit was removed deliberately: under `cpu: "1"`, **248 of 360 sessions** measured before that
change were CFS-throttled, and every hold latency was an artefact of the quota. Post-removal runs
show **0 throttled sessions** in every run.

### 2.2 What a session actually costs (measured)

CPU per session, from `cpu_usec` in the run databases. In `--layer session` runs this value has
already been divided by the number of contexts in flight, so it is a **share of the pod**:

| Run | workload | n | core-seconds p50 / p95 / max |
|---|---|---|---|
| `20261031T010911Z` | 30 sessions, 0.5/s, 30s holds, `about:blank` | 30 | 1.26 / 5.56 / 6.10 |
| `20261031T024806Z` | 600 sessions, 2/s, mix (mean 75s) | 599 | 1.45 / 5.35 / **16.13** |
| `riot-20261031t174625` | 3 sessions, real Riot pages | 3 | 4.67 / 9.26 / 9.26 |

Per-context cost, from CDP (not a share, measured on the session's own target):

| Page | task time p50 | source |
|---|---|---|
| `about:blank` | **1–2 ms** | most runs |
| splash JPEG (ddragon) | **54 ms** average | 300-session soak |
| full SPA (leagueoflegends champion page) | **3 983 ms** | same run, 1 session |

**Consequence for the investigation:** a run of `about:blank` sessions measures Chromium's idle
footprint plus the platform, not browsing. Any density number must say which page class it used.
The spread is the interesting part — p50 1.45 core-seconds against a max of 16.13 means the fleet
is bursty, which is precisely the case oversubscription is supposed to exploit.

### 2.3 Where the current ceiling comes from

| Constraint | Limit | Evidence |
|---|---|---|
| Scheduler (CPU requests) | ~21 browsers = **168 contexts**; 27 if nothing else ran | `FailedScheduling … Insufficient cpu` in `20261031T024806Z` at 21 instances |
| Node memory | 16 GiB; 21 browsers ≈ 7.3 GiB plus system | `min available 17.9%`, swap 4.1 GiB |
| Control plane (kine/SQLite) | **~2 sessions/s** of write churn | apiserver `connection refused`; see note 18 §4 |
| Pool scaling | 5 s tick, claim-count driven, **blind to CPU** | `experiments-wnj` |

That last row is the leverage for this study: **the pool grows because sessions are queued, never
because the node is hot**, and it will happily scale into `Insufficient cpu`.

---

## 3. What the CPU-oversubscription study should answer

1. **Is 100m the right request?** A session's p50 is 1.26–4.67 core-seconds *over its whole hold*,
   so the instantaneous draw is small when idle and can exceed a core during a load. Requests
   determine packing; the wrong request either wastes cores or invites scheduling refusal.
2. **How far can requests be oversubscribed before latency degrades?** With no CPU limits there is
   no throttling to observe — the signal is **latency**: `hold` vs `task_duration`, `queue wait`,
   and the PSI counters (`cpu.pressure some/full`) that the sampler already records.
3. **Does the node become the bottleneck, or kine first?** At 2 sessions/s kine falls over. Any
   oversubscription win above that rate is unusable until `experiments-0fo` (kine metrics) and
   `experiments-9kl` (fewer writes per session) land.
4. **Is there a per-page-class request profile worth splitting?** SPA sessions cost ~74× a splash in
   main-thread time. One request value for both is a compromise in either direction.

## 4. Suggested experiment ladder

Same workload shape, real pages, one variable per step. Each step: one run, keep the DB + JSON.

| Step | Knob | Measure |
|---|---|---|
| 1 | `RIOT_HOLD=30s RIOT_RATE=1` (30 concurrent) with the current 100m request | baseline: task, cpu share, PSI, 0 failures |
| 2 | same workload, requests patched to **200m** | does latency drop or stay flat? if flat, requests are already enough |
| 3 | same workload, requests patched to **50m** | scheduler still accepts more pods; watch `FailedScheduling` and per-session CPU |
| 4 | `RIOT_RATE=2` (60 concurrent) at the best request value | where does latency break: p95 hold, cpu.pressure full |
| 5 | heavy-page fraction raised (currently 1 of N) | isolates SPA cost from splash cost |

The runner already records everything the first four need; no code change is required for 1–5.
What *is* missing is the scaling signal (`experiments-wnj`) and kine visibility
(`experiments-0fo`) — without the latter, a step that fails will say "connection refused".

> **Caveat to state in any report:** requests are a *scheduling* declaration. With CPU limits
> removed, oversubscription changes admission and placement, not enforcement. Two pods each
> requesting 50m can still consume 2 cores at the same moment; nothing stops them until the node
> runs out of real CPU, at which point every session on it slows down together.

## 5. Facts that will save you time

- **Warm adoption is ~250 ms; cold start ~12–80 s.** A run that starts with the pool at 0 pays cold
  starts on the first acquisitions (`cold` in the acquisition percentiles).
- **The pool is usually at 2–3** between runs (autoscaler `min`). Check `pool desired/ready` in the
  preflight line before interpreting an acquisition percentile.
- **The runner retries** claim create and delete on transport errors (EOF, refused, timeout, 5xx),
  five attempts with backoff. A control-plane blip therefore shows as latency, not failure.
- **Report invariants:** outcomes always sum to the planned session count; `elasticity:` says
  `not tested` when the workload never exceeded what the platform served — that is a result, not a
  missing line.
- **Session ≠ slot ≠ pod ≠ claim.** "instances 21, sessions/instance max 37" is a *total per
  browser over the run*, not concurrency (ceiling was 8). Note 18 §1 has the table; issue
  `experiments-ujf` is the naming fix.

## 6. Bugs found the hard way — do not reintroduce

| Symptom | Cause | Guard |
|---|---|---|
| 30 successes recorded as 30 `timeout` | `hold()` returned two unnamed booleans, call site bound the wrong one | `holdResult` struct + regression test |
| acquisition p95 = 266 s | `ResourceVersionMatch` on a **Watch** (List-only) → every watch rejected → retry poll | `TestWatchOptionsAreAcceptedByAWatcher` |
| per-session memory = pod memory | `divideReading` divided CPU and not memory | `TestDivideReadingSharesCpuAndMemory` |
| `--out is required` from the script | `${arr[@]/--riot/}` leaves an **empty** argument; Go's flag parser stops there | riot removed from the script entirely |
| stale `paused=disabled` freezing the pool | ownership only released on a clean exit | `TestFixedPoolRunTakesAndReleasesPoolOwnership` |
| pool inflated to 5 for 3 browsers used | autoscaler counted claims while browsers carry 8 sessions | `TestDemandIsSlotsNotClaimsWhenBrowsersAreShared` |

## 7. Where not to touch

- `apps/browser-sandbox/*.py` — the bridge and forwarder. They work; the Go path runs beside them.
- `apps/session-lab/test/` — riot pages live here, test-only; the runtime never sees a URL list.
- Grafana folder/provider and the VictoriaMetrics scrape config are mid-change
  (`experiments-9b0`); coordinate before editing.
- No CPU **limits** — the policy and its evidence are in note 12. Requests are fair game; limits are
  not.

## 8. Reading list, in order

1. `18-capacity-elasticity-and-kine.md` — the ceilings, the 24 h arithmetic, why kine binds.
2. `16-browser-contexts-and-slots.md` — slots, contexts, and what sharing costs.
3. `12-cpu-requests-not-limits.md` — why there is no CPU limit, with the 248/360 throttling data.
4. `11-verification.md` — what was verified live, and what is still unknown.
5. `17-cdp-client-in-go.md` — the measurement transport, if you need per-context numbers.
