# Note 08 — Requirement status, verified

| | |
|---|---|
| Status | Audit, evidence-based |
| Date | 2026-10-29 |
| Branch | `research/session-lifecycle-elasticity` |
| Method | source reads with line references, `go test ./...`, live `kubectl`, VictoriaMetrics/Grafana manifests, and the seven run records under `docs/session-lab-runs/` |

Four columns, kept separate on purpose: **implemented** (code exists), **deployed** (something
runs), **configured** (settings are set somewhere real), **verified** (a run or a command showed it).

---

## Requirement 1 — session scheduler with exclusive ownership

| | |
|---|---|
| Implemented | `internal/run/run.go` — arrival dispatch per bucket, one goroutine per session unless `--workers`, exclusive lease through the free list, per-transition SQLite writes, terminal outcome per session, cleanup on cancel |
| Deployed | runs on the host from this checkout; 7 run records exist |
| Configured | `--sessions --mix --rate --workers --session-deadline` |
| Verified | Overload run `20261029T161101Z`: 60 planned / 60 stored, 42 success + 17 acquisition_failed + 1 reset_failed = 60. `TestConcurrencyNeverExceedsInstances` proves the peak never exceeds instances created |

## Requirement 2 — configurable pool size, configured vs observed capacity

| | |
|---|---|
| Implemented | `--pool-replicas` patches `spec.replicas` at preflight and restores the original on exit (`run.go:183`, `:663`) |
| Deployed | yes — five runs resized the pool and put it back; live pool is `spec.replicas: 0` right now |
| Configured | operator flag, recorded in each run row |
| Verified | Observed boundary, overload run: 17 refusals with `0/1 nodes are available: 1 Insufficient cpu`, measured peak concurrency 18 against an offered 60. **Configured 20 was never reached**: the pool never had 20 ready before the scheduler refused. Configured limit = 20, observed limit = 18 concurrent, binding constraint = node CPU requests |

## Requirement 3 — real elasticity

| | |
|---|---|
| Implemented | **nothing** |
| Deployed | `kubectl get hpa -A` → `No resources found`; KEDA CRDs `0` |
| Configured | upstream examples only, in the fork checkout, unapplied |
| Verified | not applicable — there is no autoscaler to verify |

Gap, and the largest one. The upstream examples scale on GKE Managed Prometheus, which this cluster
does not run. The smallest viable substitute is a controller that reads pending-claim demand and
patches the pool's replica count, with configured min/max and its own metrics.

## Requirement 4 — lifecycle experiments: reuse/reset vs destroy/rebuild

| | |
|---|---|
| Implemented | `--policy reuse\|destroy\|suspend`; reset via the in-pod bridge; delete-then-recreate on `destroy`; suspend/resume via `operatingMode` |
| Deployed | reuse and suspend have run; **destroy has never run** |
| Configured | policy recorded in every run row |
| Verified | reuse: reset p50 35 ms, 625 ms p95 at 18 concurrent (`20261029T161101Z`). suspend: resume p50 2853 ms over 5 cycles (`20261029T162956Z`). destroy: **unverified** |

## Requirement 5 — overload: queueing, refusal, scale-out, delay, recovery

| | |
|---|---|
| Implemented | unbounded queue, boundary recording, replica sampling, host memory series |
| Deployed | yes |
| Configured | `--overload-factor` / `--rate` |
| Verified | queueing and refusal: `20261029T161101Z` — queue wait p50 3.2 s, p95 134 s, 17 refusals. **Scale-out, scale-out delay and recovery: unverified** (nothing scales) |

## Requirement 6 — Prometheus metrics and Grafana dashboards

| | |
|---|---|
| Implemented | 15 metric families (`internal/metrics/metrics.go`), 10 dashboard panels (`config/grafana/dashboards/session-lab.json`), scrape job (`config/victoriametrics/templates/configmap-scrape.yaml`) |
| Deployed | scrape job is in the chart; Grafana is scaled to 0/0 and VictoriaMetrics is 1/1 — **the dashboard has never been rendered against real series** |
| Configured | `sessionLab.targets: 192.168.0.28:9109` |
| Verified | endpoint: 34 series served during a run (curl against `127.0.0.1:9109`). Dashboard: JSON parses, chart renders it — never seen with data. **No scaling panels** |

## Requirement 7 — results correlated with configuration

| | |
|---|---|
| Implemented | run row stores the full config JSON, template requests/limits, replica trajectory, host series; report prints all of it |
| Deployed | yes |
| Configured | recorded per run |
| Verified | every report carries `template browser requests cpu=100m mem=256MiB limits cpu=1000m mem=768MiB`. **Autoscaling settings: not recorded, because none exist** |

## Requirement 8 — reproducible tests and reports with evidence

| | |
|---|---|
| Implemented | 7 test files, `go test ./...` green; notes 04–08; per-run SQLite + JSON |
| Deployed | runs are local, `docs/` is gitignored |
| Configured | `--out` |
| Verified | this document's commands |

---

## What is missing, in the order it will be built

1. **A pool autoscaler** (requirement 3), with configured min/max, demand signal from pending claims,
   its own metrics, and its settings written into the cluster so a run can record them.
2. **The destroy/rebuild run** (requirement 4), same workload as the reuse overload run — 60
   sessions, mix 30s:40,2m:15,5m:5, rate 0.8/s, pool 20 — so the two differ only by policy.
3. **An elasticity run** (requirement 5): same workload, autoscaler enabled from a small pool, so
   scale-out, its delay and the recovery are measurable.
4. **Scaling panels** (requirement 6) over the autoscaler's series and the replica trajectory.
5. **The results note** (requirements 5, 7) with all three runs side by side.
