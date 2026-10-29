# Note 06 — Audit: planned vs. actually built

| | |
|---|---|
| Status | Audit, read-only |
| Date | 2026-10-29 |
| Branch | `research/session-lifecycle-elasticity` at `d60284c` |
| Method | source reads with line references, `go test ./...`, and read-only `kubectl` against the live cluster |
| Scope | Requirements of [04-deliverable.md](04-deliverable.md) and the instrumentation ask that followed it |

---

## Verdict

**Built and working.** The runner exists and does a session lifecycle end to end against
the live cluster: it generates a workload, leases one sandbox per session, holds, resets
or releases, and writes every transition to SQLite plus a JSON report. A live two-session
run produced real numbers (`05-first-run.md`).

**Not built: the observability half.** The runner exports no metrics. There is no
`/metrics` endpoint, no scrape job for it, and no dashboard for sessions. Everything the
runner knows lives in one SQLite file and one JSON file per run.

**Not built: autoscaling.** No HPA, no KEDA, no custom scaler in the cluster. What the
spec called "controlled scaling" is the runner patching the pool's replica count by hand.

**Not demonstrated: the capacity boundary.** No run has yet offered more load than the
platform could serve, so the elasticity column of every report so far reads "not tested".

## 1. Session scheduler

| | |
|---|---|
| Planned | Instance assignment, exclusive ownership, lease duration, release, cleanup, concurrency, queueing, timeouts, crash recovery |
| Implemented | `internal/run/run.go` — dispatch at arrival offsets (`dispatch`), one goroutine per session unless `--workers` is set, exclusive lease through the free list (`take`/`put`), terminal outcome written per transition (`settle`), cleanup on a context that survives cancellation (`releaseAll`, `run.go:518`) |
| Live | Exercised by one two-session run: 2/2 `success`, concurrency peak 1 (`05-first-run.md`) |
| Verified | `internal/run/run_test.go`: `TestEverySessionGetsATerminalOutcome`, `TestConcurrencyNeverExceedsInstances`, `TestReuseServesManySessionsOnOneInstance`, `TestRefusalIsRecordedAndAccounted`. All pass |
| Gap | **No lease duration.** A session with no `--session-deadline` can wait for Ready forever (`WaitReady` has no bound). **No queue limit** — `run.go:4` states the queue never rejects anyone. **Crash recovery is per-row durability, not recovery**: `store.go` commits each transition (`journal_mode=WAL`, `synchronous=FULL`) and `store_test.go` proves a reopen sees the last write, but no test kills a run mid-flight and checks for orphaned claims |

## 2. Pool management

| | |
|---|---|
| Planned | Initial size, min/max, who enforces the maximum, refill behaviour, behaviour when exhausted |
| Implemented | Preflight reads the pool and, when `--pool-replicas` is given, patches `spec.replicas` and waits for `ready == desired` (`run.go:162-200`); the original count is restored on exit, including on failure (`run.go:663`) |
| Live | `kubectl -n sandbox-demo get sandboxwarmpool browser-pool` → desired 0, ready 0. The dry run scaled 0 → 2 → 0 in 2.0 s and restored correctly |
| Verified | `TestPoolReplicasAreRestored` |
| Gap | **No min and no max anywhere in the runner.** `--pool-replicas` accepts any non-negative value (`config.go:58`, `:162`). Creation, refill and exhaustion are the upstream controller's behaviour and are not instrumented or asserted here. Preflight waits up to 30 minutes and then starts anyway (`run.go:198`) |

## 3. Autoscaling

| | |
|---|---|
| Planned | Is HPA/KEDA installed, configured, connected to a usable signal, and actually scaling |
| Implemented | Nothing. The runner patches replicas itself |
| Live | `kubectl get hpa -A` → `No resources found`. KEDA CRDs: `kubectl get crd | grep -c keda` → 0. The upstream examples exist only in the fork checkout (`examples/hpa-swp-scaling/`, `examples/keda-scale-to-zero/`), unmodified and unapplied |
| Verified | Nothing |
| Gap | **Autoscaling does not exist in this cluster.** No scaling signal, no min/max replicas, no cooldowns, no scale-down. Elasticity configuration 2 of the spec resolves to "reported as absent", which is the honest outcome, and configuration 3 is manual |

## 4. Elasticity

| | |
|---|---|
| Planned | Demand above capacity: do sessions wait, fail, or trigger scale-out; is there a queue limit; what if scaling cannot keep up |
| Implemented | Unbounded waiting queue, `first_boundary` in the report, boundary records for throttling, OOM, scheduler refusal and eviction, replica trajectory sampling |
| Live | Never exceeded. The dry run offered concurrency 1.0 against 2 replicas, and every report so far ends `elasticity: not tested` |
| Verified | Only the mechanism: `TestRefusalIsRecordedAndAccounted` proves a refusal becomes a row with a reason and the counts still balance |
| Gap | **The capacity boundary has never been observed.** No overload run exists. Whether new instances arrive before waiting sessions give up is unknown. Scale-down is the runner's restore patch, not the platform's behaviour |

## 5. Chromium lifecycle

| | |
|---|---|
| Planned | Reuse/reset, blank-page navigation, destroy/recreate, suspend/resume; which paths work, which preserve state, which were tested |
| Implemented | `PolicyReuse` resets through the in-pod CDP bridge (`run.go:361`, `adapter/kube.go` `Reset` → `POST /cdp` `Page.navigate about:blank`); `PolicyDestroy` deletes the claim (`run.go:382`); `PolicySuspend` patches `spec.operatingMode` (`run.go:371`, `:282` for resume) |
| Live | Reuse only: two sessions on one instance, reset p50 7 ms |
| Verified | Reuse: live run. Destroy: `TestEverySessionGetsATerminalOutcome` (fake adapter). Suspend/resume: **no test, no live run** |
| Gap | Suspend/resume is untested code. Whether in-guest browser state survives a suspend is unmeasured, and the code makes no claim about it |

## 6. Resource measurement

| | |
|---|---|
| Planned | Per-session and per-instance CPU/memory, throttling, OOM, pressure, sampling correctness, and the template's actual requests and limits in the report |
| Implemented | `internal/measure/measure.go`: `cpu.stat` (`usage_usec`, `nr_throttled`, `throttled_usec`), `cpu.pressure`, `memory.current` with working set derived as `current − inactive_file`, `memory.stat` refaults, `memory.events` `oom_kill`, `memory.swap.current`; host `/proc/pressure` and `/proc/meminfo`; `metrics.k8s.io` as a cross-check whose availability is recorded as a run event |
| Live | Measured: 1.19 core-seconds p50, 140 MiB p50 peak, 148 MiB working-set max, 2 of 2 sessions throttled, node `MemAvailable` floor 8929/15924 MiB |
| Verified | `internal/measure/measure_test.go` parses a synthetic cgroup; the live run proved the real cgroup path (`kubepods-burstable-pod<uid with underscores>.slice`) |
| Gap | **Template requests and limits are not recorded.** Preflight logs pool and node only (`run.go:171-173`); the report counts throttled sessions (`report.go` `Resources.ThrottledSessions`) without the limit that caused them. The OOM path has never fired. `metrics.k8s.io` is a capability record, not a second sample series |

## 7. Grafana observability

| | |
|---|---|
| Planned | Runner metrics exported to Prometheus with low-cardinality labels, scrape configuration, dashboards for throughput/concurrency, CPU/memory, pool utilisation, queue wait, lifecycle percentiles, failures, scaling |
| Implemented | **Nothing on the runner side**: `rg -c "prometheus\|promhttp" -g '*.go'` in `apps/session-lab` returns no matches. Output is SQLite plus JSON (`report.go:270`) and stdout (`report.go:279`) |
| Live | VictoriaMetrics 1/1 in `observability`; its scrape config has five jobs — `cilium-agent`, `cilium-hubble`, `cilium-operator`, `hubble-relay`, `assistant` (`config/victoriametrics/templates/configmap-scrape.yaml:15-73`) and none for sessions. Grafana is scaled to 0/0. Six dashboards exist in `config/grafana/dashboards/`, all Cilium/Hubble |
| Verified | Nothing beyond the existing scrape jobs |
| Gap | **The whole item.** No metrics endpoint, no scrape job, no dashboard, and Grafana is currently off. No OpenTelemetry configuration exists in this repository (`rg -ril "opentelemetry\|otel" config/ apps/session-lab/` → no files) |

## 8. Experiment runner

| | |
|---|---|
| Planned | Configurable session count, arrival rate, hold, concurrency, pool size, overload factor, policy, and repeatable comparisons |
| Implemented | 18 flags in `internal/config/config.go`: `--sessions`, `--mix`, `--hold`, `--rate`, `--fixed-arrival`, `--overload-factor`, `--policy`, `--pool-replicas`, `--workers`, `--session-deadline`, `--sample-seconds`, `--cgroup-root`, `--out`, `--dry-run`, `--kubeconfig`, `--context`, `--run-id`, `--namespace`/`--pool`/`--template` |
| Live | Two dry runs |
| Verified | `internal/schedule/schedule_test.go` (bucket counts, interleaving, overload factor, no bucket refused) |
| Gap | **No comparison command.** Run-to-run comparison is left to reading two JSON files. The run row stores the configuration (`store.go` `StartRun`, `model.RunConfig`) but not the resolved template limits, so two runs that differ only by a template cannot be told apart from their rows |

## 9. Results

| | |
|---|---|
| Planned | Throughput, active/waiting sessions, queue wait, acquisition/creation/deletion/reset/resume latency, CPU/memory peaks, failure categories, scaling latency, observed capacity boundary |
| Implemented | All of it in `internal/report/report.go`: outcomes, concurrency mean/peak, queue/acquire/hold/reset/release percentiles, CPU core-seconds, memory and working-set peaks, throttled/OOM/refault counts, host memory floor, instances and reuse, replica trajectory, `first_boundary`, `next` |
| Live | One two-session run, values in `05-first-run.md` |
| Verified | The report built from that run; `report.Build` is covered indirectly by `TestConcurrencyNeverExceedsInstances` |
| Gap | Most fields have only ever held two sessions' worth of data. Scaling latency is recorded as a replica trajectory, not as trigger → replica → Ready latency. No capacity boundary exists to report |

## What to fix, in order

1. **Metrics endpoint on the runner.** `promhttp` on a local port, low-cardinality labels only (policy, bucket, outcome, launch_type), one gauge per active and waiting session, histograms for queue/acquire/hold/reset/release, counters for outcomes, and per-session CPU/throttle/OOM as counters. Session identity stays in SQLite.
2. **Scrape job.** Add `session-lab` to `config/victoriametrics/templates/configmap-scrape.yaml` next to `assistant`, and record the scrape target in the run row so a run points at its own series.
3. **Template limits into the run.** Read the `SandboxTemplate` CPU/memory requests and limits at preflight and store them in the run row and the report, so a throttled session carries the limit that throttled it.
4. **One Grafana dashboard** built from the metrics above: throughput, concurrency, queue wait, lifecycle percentiles, failures, pool desired/ready, and the throttle count next to the limit.
5. **The overload run.** `--overload-factor` raised against a pool sized from what the node reports, until the platform refuses. That run is what turns "not tested" into a boundary, and its numbers replace every inherited figure.
6. **Suspend/resume, measured.** One run with `--policy suspend`, and a check of what survived.

## How to reproduce this audit

```bash
# no metrics endpoint exists
rg -c "prometheus|promhttp" -g '*.go' apps/session-lab

# nor a scrape job for it
rg -n "job_name" config/victoriametrics/templates/configmap-scrape.yaml

# the suite the Verified column refers to
cd apps/session-lab && go test ./...

# the live facts
kubectl get hpa -A
kubectl get crd | grep -c keda
kubectl -n sandbox-demo get sandboxwarmpool
kubectl -n observability get deploy
```
