# session-lab

A session scheduler and experiment runner for the sandbox platform. It generates a
session workload, leases one sandbox per session, holds it for the session's length,
releases or resets it, and writes what happened — lifecycle timings, CPU and memory,
queue wait, and the constraint that broke first.

The design notes behind it are in
[`../../research/session-lifecycle-elasticity/`](../../research/session-lifecycle-elasticity/README.md).

## What it does not do

It imposes no ceiling. There is no maximum concurrency, no maximum session count and no
clamp on the pool size: `--sessions`, `--rate`, `--mix`, `--workers` and `--pool-replicas`
are whatever the operator asks for. It also takes no number from a document — node
capacity, pool state and template shape are read from the API at start-up, and everything
in the report comes from the run's own rows.

It does not drive the console at `apps/sandbox-console/` and does not read its numbers.

## Build and run

```bash
cd apps/session-lab
go build ./cmd/session-lab

# a two-session smoke test against the live cluster: creates real claims, then
# deletes them and puts the pool back where it found it
./session-lab --dry-run --kubeconfig ~/.kube/config --out ~/runs

# four length buckets, offered load four times one instance's throughput
./session-lab \
  --mix 30s:100,2m:100,5m:50,10m:25 \
  --overload-factor 4 \
  --policy reuse \
  --pool-replicas 8 \
  --namespace sandbox-demo --pool browser-pool \
  --kubeconfig ~/.kube/config --out ~/runs
```

`--out` holds two files per run: `<run-id>.db` (SQLite, one row per session plus every
event) and `<run-id>.json` (the report). The SQLite file is written transition by
transition, so a run that dies mid-flight still has every row it reached.

## Flags that change the experiment

| Flag | Meaning |
|---|---|
| `--sessions N` / `--mix 30s:25,2m:25,…` | workload size, or length buckets with counts |
| `--hold 5m` | the hold duration for a single-bucket run |
| `--rate R` | arrival rate in sessions/s; `0` derives it from the buckets |
| `--fixed-arrival` | space arrivals evenly instead of Poisson |
| `--overload-factor F` | offered load as a multiple of one instance's throughput |
| `--policy reuse\|destroy\|suspend` | what happens to the instance between sessions |
| `--pool-replicas N` | replica count to set on the warm pool before the run; restored on exit |
| `--workers N` | bound the goroutines; `0` means one per live session |
| `--session-deadline D` | a deadline on one session; off by default |
| `--sample-seconds S` | spacing of the resource samples |

## What it measures

Lifecycle: arrival, claim create, Ready, hold start and end, reset, delete, gone — plus
the derived queue wait, acquisition, hold, reset and release distributions.

Resources: `cpu.stat` (`usage_usec`, `nr_throttled`, `throttled_usec`), `cpu.pressure`,
`memory.current` with working set derived as `current − inactive_file`, `memory.stat`
(`anon`, `file`, `shmem`, `workingset_refault_*`), `memory.events` (`oom_kill`) and
`memory.swap.current`, read from the pod's cgroup slice on the host. `metrics.k8s.io` is
read as the platform's own cross-check, and whether it answered is recorded in the run.

Boundaries: per-session OOM kills and CFS throttling, scheduler refusals, evictions, and
the node's `MemAvailable` floor over the run.

## Tests

```bash
go test ./...
```

The tests use an in-memory adapter and a synthetic cgroup directory, so they need no
cluster and no root.

## How it reads the API

The runner and the autoscaler use Kubernetes watches, not polling. Two sites
matter (`internal/adapter/watch.go`):

| Read | Before | Now |
|---|---|---|
| Waiting for a session's claim to become Ready | `Get` the claim every 1 s, per waiting session | one `Watch` on that claim name, from the `resourceVersion` of an initial `Get` |
| The autoscaler's demand counts | full `List` of claims every 5 s | one `List` at start, then a watch updates a map; the tick reads the map |

A 150-session run used to put roughly 9 000 claim `GET`s a minute through a
single apiserver on six cores; the same run now issues one watch per waiting
claim and reads events. Interruptions are handled rather than assumed away: a
watch that closes or returns `410 Gone` re-lists and resumes from the new
`resourceVersion`, with backoff, and `ClaimStats` falls back to a direct list if
the cache is not warm — a broken watch must not read as zero demand.

Timestamps come from the event (`ReadyNS` is recorded when the transition is
seen), so queue and acquisition latencies are no longer quantised to a
one-second poll. The cgroup, PSI and host-memory samplers stay on their 2 s
timer: they are not API objects.

## Package map

```
cmd/session-lab/          the experiment runner binary
cmd/pool-autoscaler/      the capacity binary
internal/runtime/         DEMAND plane: schedule, session (lease/hold/release), report
internal/infra/           CAPACITY plane: sandbox (interface + fake), kube (client),
                          pool (pool sizing), measure (cgroup, metrics-server, boundary)
internal/platform/        shared: config, model, store (SQLite), metrics (Prometheus)
```

`internal/runtime` imports `internal/infra/sandbox` (the interface) and never
`internal/infra/kube`, so the scheduler and the session lifecycle can be tested
without a cluster. Only the binaries wire the real client.

## Two layers to load-test

| Layer | Command | One pod carries | Measures |
|---|---|---|---|
| infra | `scripts/session-lab-experiment.sh --layer infra ...` | one session | claims, sandboxes, pod scheduling |
| session | `scripts/session-lab-experiment.sh --layer session ...` | up to 8 sessions | browser contexts, per-session cost inside a shared browser |

`--contexts-per-browser N` sets the packing directly; `1` is the historical one-claim-per-session
shape. With N > 1 each session gets its own `Target.createBrowserContext` inside the Chromium and
the context is disposed at the end, so a session starts with no cookies, storage or cache from the
one before it. The cgroup counter is the pod's, so with sharing the per-session CPU is the pod's
usage divided by the contexts in flight, and the row is marked `cpu_shared`.
