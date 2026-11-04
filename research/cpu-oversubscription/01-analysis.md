---
title: '01 — Our implementation, traced'
date: 2026-10-31
kind: analysis
state: unknown
commits: [30df98e]
tags: [cpu, state-unknown]
---

# 01 — Our implementation, traced

Everything here was read from the code on `research/session-lifecycle-elasticity`; file paths are
current as of `30df98e` plus the boot-permit change.

## The path a request actually takes

```
dispatch (runtime/session/run.go:319)          one goroutine per planned session, offset-scheduled
   │
   ▼
admission.Gate.Acquire (runtime/admission/admission.go:96)
   │  bound = --admit, default 0 = unbounded; orders by class only
   ▼
boot permit (runtime/session/shared.go, new)   --boot-concurrency, default 0 = unbounded
   │  held until the browser has a usable context, not just until the claim exists
   ▼
slots.Pool.Take (runtime/slots/slots.go:74)    one provisioning at a time, enforced
   │  returns (instance, slot) or calls the provider
   ▼
acquireInstance (runtime/session/shared.go)    Acquire + retry(5, backoff) + WaitReady(watch)
   │
   ▼
SandboxClaim POST  ───────────────────────────► kine write #1
   ▼
controller: warm adoption (~250 ms) or cold start (12–80 s)
   ▼
Chromium boots ─────────────────────────────── CPU spike, unmeasured until now
   ▼
OpenSession: Target.createBrowserContext + createTarget(url)   (runtime/browser)
   ▼
hold (run.go:625)  cgroup sample every --sample-seconds: cpu.stat, memory.*, PSI
   │
   ▼
CloseSession → slots.Give → reuse keeps the browser, destroy deletes the claim   kine write #2
```

## Ownership and signals, stage by stage

| Stage | Owner | Reads | Decides | On exhaustion |
|---|---|---|---|---|
| admission | `runtime/admission` | class, `--admit` | queue order, how many are in flight | waits (never rejects) |
| boot permit | `runtime/session` | `--boot-concurrency` | how many startups run at once | waits |
| slot allocation | `runtime/slots` | free slots per instance, `--contexts-per-browser` | reuse a slot or ask for capacity | asks for capacity |
| claim creation | `infra/kube` + API | pool, name | creates a claim; retries transport errors | recorded as `acquisition_failed` |
| pool sizing | `cmd/pool-autoscaler` | `ready+pending` claims ÷ contexts | replicas, clamped min/max every 5 s | stops at `--max` |
| node scheduling | kube-scheduler | pod **requests** (100m, 256Mi) | places the pod | `FailedScheduling` (recorded as a boundary) |
| browser boot | Chromium | — | — | slow, CPU-hungry |
| measurement | `infra/measure` | `/sys/fs/cgroup`, `/proc/meminfo`, `/proc/stat`, metrics-server | — | miss, not error |

## Where concurrent decisions can disagree

1. **Admissions vs CPU.** With `--admit 0` every arrival is admitted immediately. Twenty concurrent
   admissions all observe the same (cool) node and all start browsers. This is the TOCTOU the
   Gemini paper describes, and it exists here in exactly one form: *simultaneous boots*.
2. **Autoscaler vs scheduler.** The autoscaler sizes from claim demand and cannot see
   `Insufficient cpu`. In `20261031T024806Z` it scaled toward 40 replicas for ~21 browsers worth of
   work, and the extra pods' 100m requests starved the scheduler (`FailedScheduling` at 23:59).
3. **Slot pool vs everything.** `slots.Pool` serialises creation, so it does not duplicate claims —
   this is the one place where a race was already designed out.

## What is measured, and what is not

| Signal | Where | Granularity | Covers startup? |
|---|---|---|---|
| `cpu_usec`, `throttled_usec` | per session, cgroup | per hold | **no** — needs `PodUID`, filled at Ready |
| `memory.current`, `memory.stat`, `memory.events` | per session | per hold | no |
| `cpu.pressure`, `memory.pressure` | per session | per hold | no |
| `task_duration`, `script_duration`, `js_heap` | per session, CDP | per session | no (measured after open) |
| `session_lab_node_cpu_cores` | whole node, `/proc/stat` | 1 s | **yes** (added with this study) |
| `session_lab_instances_booting` | runner | per boot | **yes** (added) |
| `session_lab_boot_seconds` | runner | per boot | **yes** (added) |
| kine SQL latency | apiserver `/metrics` | scrape | n/a — not yet scraped |

The three added series are the smallest measurement that answers "do startup spikes overlap on this
node?" — before them the answer could only be inferred from acquisition latency.

## Arithmetic that constrains any oversubscription

Node allocatable: 6000m CPU, 15 550 MiB memory, 110 pods. Non-browser baseline: ~721m, ~1.5 GiB,
~44 pods.

| Limit | Browsers that fit |
|---|---|
| CPU requests at 100m | (6000 − 721) / 100 ≈ **52** |
| CPU requests at 50m | (6000 − 721) / 50 ≈ 105 → but pods cap at 110 − 44 = **66** |
| Memory requests at 256Mi | (15550 − 1600) / 256 ≈ **54** |
| Pod count cap | **66** |
| Actual memory per 8-context pod | ~360 MiB, i.e. the 256Mi request is under-declared by 40 % |

**So halving the CPU request moves the ceiling from 52 to 54 — memory binds.** The experiment is
still worth running, because the measurement that proves it is also the one that shows whether CPU
pressure behaves as expected.

## Startup burst: what we can and cannot say today

- Cold boot measured end to end: **12–80 s** (`ready (cold)` in run logs), warm adoption **~250 ms**.
- CPU during boot: **unmeasured per pod**. The cgroup needs the pod UID, which only exists once the
  sandbox is bound and Ready.
- Node CPU during boot: measurable from this change on, at 1 s resolution, together with the boot
  concurrency gauge. Two series are enough: if `node_cpu_cores` rises while `instances_booting`
  rises, spikes converge.
- Whether a spike is 200m or 500m per boot: **UNKNOWN**, and it is the first thing the ladder should
  establish — with `RIOT_HEAVY` and `RIOT_BOOT` that is now possible without new code.

## Request floors, measured (added after the first boot measurement)

`memory.peak` of the browser pod's cgroup, read from the host, no sessions running:

| Pod state | memory.current | memory.peak | Source |
|---|---|---|---|
| Fresh boot, one default page, request 256Mi | 183 MiB | 190 MiB | cgroup read, `browser-pool-bjcb2` / `w754t` |
| Same, request lowered to 128Mi | 145–152 MiB | 145–151 MiB | cgroup read after recreation |
| Carrying 8 real-page contexts | — | ~360 MiB | `20261031T024806Z` per-pod peak |
| CPU | no limit (`cpu.max = max 100000`) | — | `cpu.stat` |

**What this says about requests:**

1. A `SandboxClaim` has no resource attributes of its own — it adopts a Sandbox, and the Sandbox's
   pod carries the Template's requests. So "the request for a claim" *is* the browser pod's request.
   There is no separate, smaller number to find.
2. Chromium needs **~150–190 MiB resident** just to exist with one page. A 128Mi request is
   therefore ~40–60 MiB below the pod's footprint from the moment it starts: deliberate overcommit,
   safe only while the node has physical memory to absorb it.
3. With 128Mi requested, the scheduler's memory ceiling moves to ~109 pods and stops binding. What
   binds becomes **CPU requests (~52 browsers)** and the **110-pod cap (~66 browsers)** — which is
   the intended shape of the oversubscription experiment.
4. The physical constraint does not move: 8-context browsers use ~360 MiB each, so ~13.9 GiB of
   usable node memory serves **~38 browsers** at full load regardless of what the requests say.
   Requests below reality mean the scheduler admits what the node cannot serve; the OOM killer is
   then the enforcement mechanism, which is worse than a refusal.

**Chosen values:** request `cpu: 100m`, `memory: 128Mi`; limit `memory: 768Mi`, no CPU limit. The
request is the lever under study; the limit stays the hard ceiling.

## Memory per page class — measured from an OOM (2026-10-31, `riot-20261031t191844`)

A run with `RIOT_HEAVY=10` (ten full League SPAs) lost one browser and eight sessions. The run's
own database explains it without guesswork:

| Instance | sessions | peak memory | OOM kills (`memory.events`) |
|---|---|---|---|
| `…-1` (carried 8 heavy SPAs) | 8 | **770 MiB** | **224** |
| `…-2` | 8 | 735 MiB | 0 |
| `…-3` | 8 | 591 MiB | 0 |
| `…-4` (6 sessions) | 6 | 463 MiB | 0 |

The template's memory **limit is 768Mi**. Instance 1 reached 770 MiB, the kernel OOM-killed
processes inside the pod, Chromium's DevTools socket died, and every session on that browser failed
at context close with `cdp: read: … EOF`.

Three facts worth carrying forward:

1. **Memory per context depends on the page, and heavily.** Splash pages: ~360 MiB for 8 contexts
   (~45 MiB each). Full SPAs: ~770 MiB for 8 (~96 MiB each, and these were competing).
2. **The limit, not the request, is what kills.** Requests are 128Mi and had nothing to do with it;
   768Mi was the ceiling the kernel enforced.
3. **Placement concentrated the heavy sessions.** Slots fill the first browser first, so
   `RIOT_HEAVY=10` put sessions 1–8 — the heavy ones — into one browser. Spreading heavy sessions
   across browsers is a test-side change (interleave instead of prefix), not a runtime one.

Reporting gap fixed with this: a dead browser now settles its sessions as `lost_unknown`, not
`reset_failed`, so the outcome names the cause.

---

## Where this sits

- Collection: [cpu-oversubscription](README.md)
