# Note 05 — The first run: a two-session smoke test, and the three defects it found

| | |
|---|---|
| Status | Recorded |
| Date | 2026-10-29 |
| Phase | 8 — session lifecycle elasticity |
| Command | `session-lab --dry-run --pool-replicas 2 --namespace sandbox-demo --pool browser-pool` |
| Run id | `20261029T154943Z` (report at `docs/session-lab-runs/20261029T154943Z.json`, gitignored) |
| Code | `apps/session-lab` at commit `phase 5` |
| Related | [04 — the deliverable](04-deliverable.md) |

---

## 1. What this run is, and what it is not

A smoke test. Two sessions of five seconds against `sandbox-demo/browser-pool`, with the
pool scaled from 0 to 2 replicas and restored to 0 afterwards. It exists to prove the
paths that create, adopt, hold, reset and delete real objects, and to produce the first
numbers the runner has ever reported.

Two sessions say nothing about capacity. The numbers below describe one browse-shaped
workload on this node at one instant, and the run reports no boundary because it never
looked for one.

## 2. What it measured

| Quantity | Measured |
|---|---|
| sessions planned / stored | 2 / 2 |
| outcomes | `success` 2 |
| sessions/s | 0.200 |
| offered concurrency | 1.0 |
| measured concurrency | peak 1, mean 1.0 |
| queue wait (claim → Ready) | p50 103 ms, max 103 ms |
| acquire (arrival → Ready) | p50 106 ms |
| hold | p50 5022 ms |
| reset (navigate to a blank page) | p50 7 ms |
| CPU per session | 1.19 core-seconds p50, 1.41 max, 2.60 total |
| memory per session | 140 MiB p50 peak, 152 MiB max, 148 MiB working-set max |
| CPU throttled | 2 of 2 sessions |
| OOM kills | 0 |
| workloads with refaults | 0 |
| instances | 1 total, 2 sessions on it (reuse policy) |
| node memory | MemAvailable floor 8929 MiB of 15924 MiB (56.1%), swap used 1025 MiB |
| preflight | pool 0 → 2, ready in 2.0 s, node allocatable 6000m CPU / 15923 MiB / 110 pods |

Both sessions adopted an existing sandbox (`launch_type` warm), which is why the queue
wait is ~100 ms rather than a cold start. The reuse path did what it was built for: two
sessions ran on one instance, and the reset between them cost 7 ms.

## 3. What the run exposed

Three defects, each found by executing rather than by reading.

1. **The claim name was invalid.** The default run id is a UTC timestamp
   (`20261029T142816Z`), and its uppercase letters violate RFC 1123. The API server
   rejected every claim: `metadata.name: Invalid value ... a lowercase RFC 1123 subdomain
   must consist of lower case alphanumeric characters`. Fixed in `claimName()`, which
   lowercases the id and maps anything outside `[a-z0-9-]` to a dash. The first run's
   report recorded this honestly as `acquisition_failed` with the API's message as evidence,
   which is what the outcome enum is for.
2. **The pod cgroup was never found.** `PodCgroupDir` globbed `kubepods-*.slice/*<pod-uid>*`
   with dashes. k3s's systemd driver writes the pod UID with underscores
   (`kubepods-burstable-pod0b845006_2d0c_4338_afb8_79b474fc9c40.slice`) and puts
   `cri-containerd-*.scope` below it, so nothing matched and every session reported zero
   CPU and zero memory. The reader now tries both spellings and reads the pod slice, which
   aggregates the pod's containers.
3. **A missing sample was counted as a sample.** `applyReading` was passed `r.smp != nil`
   as its "sampled" flag, so a session with no reading still incremented the sample count.
   It now receives what `hold()` actually observed.

The second defect is the one that mattered: without it the report looked complete and
said the sandbox used no CPU.

## 4. What is open

**Both sessions were CPU-throttled** (2 of 2, from `cpu.stat`). The hold latency in this
run is therefore partly an artifact of the CPU limit on the sandbox template, not a
property of the workload. The runner's own recommendation came out as: raise the limit or
remove it, then repeat.

The sandbox template's CPU limit is readable from the live object and was not recorded in
this note, because the runner does not yet write the resolved template limits into the run
row. That is the next gap to close, and it belongs in the report next to the throttling
count so a reader can see what the sessions were held to.

## 5. What the next run should be

A capacity run, not a smoke test: four length buckets, `--overload-factor` raised until the
platform refuses, `--pool-replicas` set from what the node reports rather than from any
remembered figure. The first such run should be expected to end in a boundary record —
scheduler refusal, a claim that never becomes Ready, or the node's memory floor — and that
record is the result.

## 6. How to check this note

```bash
# the run's own rows
sqlite3 <run>.db 'select id,outcome,launch_type,cpu_usec,throttled_usec,nr_throttled,mem_peak_bytes from sessions;'

# the throttling the report names
sqlite3 <run>.db 'select throttled_usec, nr_throttled from sessions where throttled_usec > 0;'

# the pool this run scaled and restored
kubectl -n sandbox-demo get sandboxwarmpool browser-pool -o jsonpath='{.spec.replicas}{"\n"}'
```
