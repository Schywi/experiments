---
title: '04 — Datastore capacity: research brief and prompt'
date: 2026-10-31
kind: analysis
state: unknown
tags: [cpu, control-plane, state-unknown]
---

# 04 — Datastore capacity: research brief and prompt

| | |
|---|---|
| Written | 2026-10-31 |
| Purpose | hand this file to a research agent: raise the sessions/s ceiling by fixing the datastore |
| Verified here | k3s journal, apiserver `/metrics`, `kubectl describe node`, run databases |
| Not verified | SQLite file size, WAL size, btrfs allocation state (no read access from this sandbox) |

---

## 1. What is measured, right now

### Datastore configuration

`/etc/rancher/k3s/config.yaml` sets no `datastore-endpoint`. k3s therefore runs its default:
**kine over SQLite**, one file under `/var/lib/rancher/k3s/server/db/`, on `/dev/sda3`
(btrfs, `compress=zstd:1`, **81 % full, 21 GB free**).

### The stalls

`journalctl -u k3s --since "-60 min"` while experimenting:

| Metric | Value |
|---|---|
| `Slow SQL` events in one hour | **818** |
| Worst commit | **39.7 s**, then 31.0 s, 30.6 s |
| Statement mix | **783 INSERT**, 30 SELECT, 5 DELETE |
| By object | **leases 460**, events 71, pods 66, cilium.io 66, masterleases 18, peerserverleases 12 |

`kine_sql_time_seconds` (apiserver `/metrics`) shows the shape: of 3 023 `AfterAllOldVal` calls,
**2 996 finish under 1 ms**. The average SQL time is ~1 ms and the tail is 40 s. **This is not
volume, it is stalls** — a filesystem/fsync problem, not a query-plan problem.

### What it costs when it stalls

| Symptom | Evidence |
|---|---|
| `POST /sandboxclaims` → `unexpected EOF` | 1 of 600 sessions in `riot-20261031t193244` |
| `lease` renewals miss → controller restarts | 119 restarts of `agent-sandbox-controller`, `leader election lost` |
| cilium-operator churn | 106 restarts |
| Acquisition latency in minutes | `20261031T024806Z`: p95 266 s (aggravated by a watch bug, since fixed) |

### Writes per session (from the run records)

Minimum five per session: claim create, sandbox status, event, claim delete, cilium endpoint
create/delete. At 2 sessions/s that is ~10 writes/s — trivial for any datastore, which is further
evidence that the ceiling is latency, not throughput.

### Why leases dominate

kine renews **leases** on behalf of etcd-style clients: the sandbox controller, cilium-operator,
kube-controller-manager, kube-scheduler. Every renew is an INSERT. When one INSERT stalls 30 s, the
lease expires, the holder exits, and its replacement writes more. That is the
positive feedback loop behind the restart counts.

---

## 2. Do we need CPU valley oversubscription? No — and here is the arithmetic

Current state: browser request **50m CPU / 128Mi memory**, no CPU limit. Node: 6 cores,
15.9 GiB, 110 pods.

| Ceiling | Browsers that fit | Sessions (8 contexts each) |
|---|---|---|
| CPU requests at 50m | (6000−721)/50 ≈ 105 | 840 |
| Pod cap (110 − 44 system) | 66 | 528 |
| **Physical memory** (360 MiB per 8-context browser) | **~38** | **~304** |
| Control plane (kine) | — | **~2 sessions/s observed** |

The node has **10.4 GiB available and CPU requests at 24 %** while the platform refuses sessions.
Browsers are not packed out; the datastore is the constraint. Oversubscribing CPU further changes
nothing that is currently binding.

What CPU work *was* needed is already in place: boot spikes measured at **1.5–1.9 cores** per cold
start, and `--boot-concurrency` bounds how many land together. That is the whole CPU story until the
datastore stops limiting.

---

## 3. Hypotheses to test (cheapest first)

| # | Hypothesis | Test | Kills it if |
|---|---|---|---|
| H1 | fsync stalls on a btrfs volume at 81 % full | write a 1 GB file and measure fsync latency; sample `btrfs filesystem usage`; check `dmesg` for allocation stalls | fsync < 10 ms consistently |
| H2 | lease churn is the amplifier | count lease INSERTs per minute with no experiments running; then with the sandbox controller scaled to 0 | lease rate unrelated to stalls |
| H3 | the datastore engine matters more than the disk | run kine over Postgres on a **different host** for one day | p99 unchanged |
| H4 | fewer writes per session raise the ceiling | stop per-pod events and reuse claims; measure sessions/s at fixed p99 | ceiling unchanged |
| H5 | SQLite is simply too small a hammer | embedded etcd (`--cluster-init`) on the same disk | same stalls appear |

---

## 4. Options, with costs

| Option | Removes | Costs | Verdict |
|---|---|---|---|
| **Free disk / move the db** | btrfs at 81 % is the prime suspect for fsync stalls | host work, no code | **do first** |
| **Fewer writes per session** (no per-pod events; reuse claims; avoid status rewrites) | 50–80 % of our own writes | runner + controller changes | **do second** |
| **External etcd on a second host** | the SQL log entirely; etcd is built for exactly this write pattern | a second machine, ~1–2 GiB RAM there, cluster re-init | **the real fix if H1 is disproved** |
| **Embedded etcd (`--cluster-init`)** | kine's SQL translation | same disk, same fsync path; ~1 GiB on this node | unlikely to help alone |
| **kine over MySQL/Postgres** | SQLite's single-file behaviour | a database to run; helps only if the stall is SQLite-specific, not disk | test H3 before paying |
| **Move SQLite to a dedicated NVMe/directory** | contention with everything else on `/` | host work | cheap experiment |

---

## 5. The research prompt

> You are a senior platform engineer. The cluster described below refuses sessions long before it
> runs out of CPU or memory. Your job is to find out why the **datastore** limits it, and to raise
> the sessions/s ceiling.
>
> **Read-only.** Inspect `/experiments`, `/agent-sandbox`, the live cluster (`kubectl`,
> `journalctl -u k3s`), and these notes: `research/cpu-oversubscription/01-analysis.md`,
> `research/session-lifecycle-elasticity/18-capacity-elasticity-and-kine.md`. Do not change
> anything; produce a plan.
>
> **Facts to verify, not assume.** k3s 1.36.5+k3s1, single node, `kine` over SQLite on btrfs at
> 81 % full; 818 `Slow SQL` events per hour, worst 39.7 s, 783 of them INSERTs, 460 involving
> `/registry/leases/`; `kine_sql_time_seconds` averages 1 ms with a 40 s tail; 5+ writes per
> browser session; ~2 sessions/s is where the API server starts refusing.
>
> **Answer, with evidence and arithmetic:**
>
> 1. Is the stall the filesystem, the engine, or the write pattern? Design the smallest experiment
>    that separates them (fsync latency, lease churn with the controller stopped, writes per session
>    at a fixed session rate).
> 2. Which single change raises sessions/s the most per unit of risk: free disk, fewer writes,
>    external etcd, embedded etcd, or another kine backend? Give the expected sessions/s for each,
>    derived from measurement, not from vendor claims.
> 3. What does a 24-hour soak at the chosen rate look like — what breaks first and how do we see it
>    before the run dies?
> 4. Is CPU oversubscription relevant at all before the datastore is fixed? Show the arithmetic
>    (the notes claim it is not, because memory requests and the control plane bind first).
> 5. What should we measure next, in what order, with exact commands?
>
> **Do not** propose Redis, distributed locks or a rewrite of the scheduler. This is a single-node
> k3s where one SQLite writer is the bottleneck, and the deliverable is a measurement plan that
> raises the ceiling, not an architecture.

---

## 6. What to do in the next hour, concretely

```bash
# how bad is the disk right now
df -h /var/lib/rancher          # 81 % used, 21 GB free
journalctl -u k3s --since "-15 min" | grep -c "Slow SQL"

# is it the leases? stop the amplifier and watch
kubectl -n agent-sandbox-system scale deploy/agent-sandbox-controller --replicas=0
journalctl -u k3s --since "-5 min" | grep -c "Slow SQL"    # compare with the run above
kubectl -n agent-sandbox-system scale deploy/agent-sandbox-controller --replicas=1

# is it fsync? one big write, timed
dd if=/dev/zero of=/var/tmp/fsync-probe bs=1M count=1024 oflag=dsync status=none
rm -f /var/tmp/fsync-probe

# then the ceiling probe: splash pages, 8 contexts, fixed hold
SESSION_LAB_INTEGRATION=1 RIOT_SESSIONS=600 RIOT_HOLD=75s RIOT_RATE=2 RIOT_HEAVY=0 \
  go test -tags integration -count=1 -timeout 2400s ./test/ -run TestRiotLive -v
```

---

## Where this sits

- Collection: [cpu-oversubscription](README.md)
