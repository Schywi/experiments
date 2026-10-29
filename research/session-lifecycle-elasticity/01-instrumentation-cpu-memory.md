# Note 01 — Measuring CPU and memory per session: cgroup v2, PSI, and where eBPF earns its place

| | |
|---|---|
| Status | Draft for review |
| Date | 2026-10-29 |
| Phase | 8 — session lifecycle elasticity |
| Scope | What to instrument so a per-session CPU and memory number exists at all. Nothing here changes the sandbox platform's code |
| Evidence base | agent-sandbox checkout `d816e0f` (see the folder [README](README.md) for provenance and the six corrected line numbers) |
| Related | [02 — objective and what a 10k-session run can claim](02-objective-and-soak-statistics.md), [03 — what exists today](03-what-exists-today.md), [04 — the deliverable](04-deliverable.md) |

---

## Status of the numbers in this note

The instrument is the subject here; the figures quoted next to it are somebody's earlier reading and
none of them is an input to a run ([04](04-deliverable.md) §2).

| Quantity | Where it came from | Status |
|---|---|---|
| `0.3 vCPU` per actively-driven Chromium | fork `research/experiments/02-capacity-6vcpu-100k.md:133-134` | that document calls it an assumption |
| a browser-per-host density figure | fork research, and Phase 7's note in this repository | unverified; replaced by run output |
| the per-pod CPU limit a sandbox carries | the live `SandboxTemplate` | read it from the object at run time |

Whatever `apps/session-lab/` measures replaces these rows, and the correction lands here next to the
run that produced it.

---

## 1. The question this note answers

A capacity sentence needs one number: CPU per actively-driven browser. The research in the
`agent-sandbox` fork uses `0.3 vCPU` and labels it an assumption
(`research/experiments/02-capacity-6vcpu-100k.md:133-134`). Every density figure and host count
downstream of that line inherits the assumption — the 9-active-browser planning density at
`:138`, the host table in the 100k-session analysis at `:146-172`.

The alternative on the table is eBPF. For CPU and memory it buys nothing that cgroup v2 files
and PSI do not already provide, and it perturbs the numbers it measures. The section that matters
is §4: four questions where tracing is the only instrument.

## 2. What the stress harness collects today

Three sources, none of them per-sandbox CPU or memory (`test/stress/promscrape.go`):

| Source | Selector | What it returns |
|---|---|---|
| node-exporter DaemonSet | `app.kubernetes.io/name=node-exporter`, `:9100` (`test/benchmarks/scenarios/benchmarks-kops-gcp/node-exporter.yaml:54-58`) | `cpu`, `meminfo`, `diskstats`, `loadavg` only. `--collector.disable-defaults` follows, so no PSI collector |
| kubelet | `/api/v1/nodes/<node>/proxy/metrics` (`promscrape.go:200-212`) | the kubelet process itself, per the comment at `:180-181` |
| cAdvisor | — | skipped deliberately: "very large, container-level resource data" (`promscrape.go:63-65`) |
| controller | `:8080/metrics` via the agent-sandbox-controller pod (`promscrape.go:157`) | the controller's own Go runtime, not sandboxes |

The samples land long-and-narrow for DuckDB (`promscrape.go:44-52`), and the report is built in
DuckDB (`test/stress/generate-report/generate_report.py:239`). Node totals are covered. Per
session is not, which is the hole this note closes.

## 3. The metric set, from cgroup v2

For each sandbox pod cgroup, `/sys/fs/cgroup/kubepods.slice/kubepods-*.slice/cri-containerd-<id>.scope/`:

**CPU**

| File | Field | Why it matters |
|---|---|---|
| `cpu.stat` | `usage_usec`, `user_usec`, `system_usec` | replaces the 0.3 vCPU assumption, per session, integrable |
| `cpu.stat` | `nr_throttled`, `throttled_usec` | a pod is throttled once it reaches `cpu.max`; read that limit from the live object, and treat a throttled session's latency as an artifact of the limit |
| `cpu.max` | `quota/period` | records what the observation was made under |
| `cpu.pressure` | `some avg10/60/300`, `full total` | was this session runnable-but-starved |

**Memory**

| File | Field | Why it matters |
|---|---|---|
| `memory.current` | — | charged total |
| `memory.stat` | `anon`, `file`, `shmem`, `file_dirty`, `file_writeback` | the anon/file/shmem split argued for at `research/experiments/03-browser-density.md:106-113`; `shmem` is the `emptyDir{medium:Memory}` trap — charged to the pod, invisible to `requests` |
| `memory.stat` | `workingset_refault_anon`, `workingset_refault_file`, `pgscan`, `pgsteal` | the thrash metric: a page evicted and needed again. Distinguishes "swap absorbs idle pages" from "swap is paid for in latency", the open question at `03-browser-density.md:389` |
| `memory.events` | `oom`, `oom_kill`, `high`, `max` | OOM death without parsing kubelet events |
| `memory.swap.current`, `memory.swap.events` | — | per-pod swap, which `fail-swap-on=false` does not report |
| `memory.pressure` | `some`, `full` | `full > 0` means every task stalled: the session was doing no work |

**The accounting trap.** OOM and eviction use `working_set ≈ memory.current − inactive_file`.
Reporting `rss` instead both over- and under-counts across a page-cache cliff and disagrees with
the kubelet about when a pod should have died. Record `memory.current`, `inactive_file`, and
derive working set.

## 4. Three collection paths, cheapest first

**A. Read the cgroup files.** Eight small files per pod per sample, one JSONL row per
`(ts, pod_uid)`, joined offline. This is the only path that yields per-session PSI: cAdvisor does
not export PSI, and node-exporter's `--collector.pressure` is host-wide. Requires cgroup v2 with
`psi=1`; check `/proc/pressure/cpu` exists before believing a zero.

**B. kubelet `/metrics/resource`.** `/api/v1/nodes/<node>/proxy/metrics/resource` returns
`container_cpu_usage_seconds_total` and `container_memory_working_set_bytes` per pod, with no
image or filesystem dimensions. Cheap enough to leave on, and a ten-line addition beside the
kubelet scrape at `promscrape.go:200-212`. It carries the standard names without cAdvisor's
cardinality.

**C. cAdvisor `/metrics/cadvisor`.** Kubernetes-standard names
(`container_cpu_cfs_throttled_seconds_total`, `container_memory_working_set_bytes`,
`container_memory_failures_total{scope=container,type=pgmajfault}`, `container_oom_events_total`),
useful when a dashboard has to match everyone else's. Cost is thousands of series per node, which
is why `promscrape.go:63-65` skipped it. The harness already writes JSONL, so a client-side
namespace predicate keeps the cardinality out of Prometheus.

One line on the node-exporter DaemonSet adds the host families the fork's density parser already
understands (`host_cpu_psi_waiting_seconds_total`, `host_mem_psi_waiting_seconds_total`,
`host_io_psi_waiting_seconds_total` — `examples/gke-swap/python-density/parse_telemetry.py:80-92`),
and `parse_telemetry.py:15` records that the GKE density study was built on cAdvisor CSV plus PSI
with no eBPF at all.

## 5. Where tracing earns its place

Four questions, all optional, and cgroup files stop at the pod boundary for each of them:

| Question | Tool | Why tracing |
|---|---|---|
| Which process inside the sandbox burned the CPU (Chrome main vs renderer vs GPU process; `--use-angle=swiftshader` burning a core, experiment B1 in `03-browser-density.md`) | `execsnoop` + `sched:sched_stat_runtime` | cgroup accounting sums the pod |
| Is the session blocked off-CPU or running | `offcputime`, `runqlat` | `cpu.pressure full` approximates this for free at pod level; per-task is where tracing starts |
| Which refaults come from Chrome's sparse cache vs the binary page cache | `cachestat`, vmscan tracepoints | `workingset_refault_*` gives the total only |
| What a "blank" reuse costs (exec/fork counts, `sched_stat_runtime`) | `sched:sched_process_exec` | prices the reuse path, §5 of `03-what-exists-today.md` |

Rules for using it, from the same ground rules the fork's experiment index states
(`research/experiments/README.md`, reproducibility):

- Maintained BCC/bpftrace tools, no bespoke loader. A hand-rolled BPF program is one more
  variable in a run whose value is reproducibility.
- No BPF output as a Prometheus label. Per-connection and per-syscall data is unbounded
  cardinality, which `AGENTS.md` forbids; aggregate to a fixed enum, keep raw rows in JSONL, join
  offline — the pattern `promscrape.go:44-52` → `generate_report.py:239` already uses.
- One control pass with probes off, so overhead is not attributed to the workload.
- Skip tracing for microVM isolation. Host-side BPF sees `clh` and `virtiofsd`, not Chrome; the
  6-vCPU host has neither `gvisor` nor `kata-*` RuntimeClass and no `/dev/kvm`
  (`research/experiments/02-capacity-6vcpu-100k.md:19-20`), so this only matters on GKE.

## 6. What to record per session

The join keys exist: `test/stress/tracker.go:108-115` records `PodUID`, `NodeName`,
`ContainerID` "for joining against node-side data sources", plus `BoundSandbox` for claims.

| Column | Source |
|---|---|
| `cpu_usec_total`, `cpu_usec_user`, `cpu_usec_system` | `cpu.stat` |
| `throttled_usec_total`, `nr_throttled` | `cpu.stat` |
| `cpu_psi_some_usec`, `cpu_psi_full_usec` | `cpu.pressure` |
| `mem_peak_current`, `mem_peak_working_set`, `mem_inactive_file_at_peak` | `memory.current`, `memory.stat` |
| `anon_peak`, `file_peak`, `shmem_peak` | `memory.stat` |
| `workingset_refault_anon`, `workingset_refault_file` | `memory.stat` |
| `swap_peak_current`, `pgscan`, `pgsteal` | `memory.swap.current`, `memory.stat` |
| `mem_psi_some_usec`, `mem_psi_full_usec`, `io_psi_some_usec` | `memory.pressure`, `io.pressure` |
| `oom_kill` (0/1) | `memory.events` |
| `samples_n`, `window_seconds` | harness |

Three invariants make the run trustworthy, in the spirit of the Little's-law table already used
at `research/experiments/02-capacity-6vcpu-100k.md:146-172`:

1. Σ `cpu_usec_total` over sandbox pods ≈ Δ `node_cpu_seconds_total{mode=user+system}` minus the
   kubelet/system baseline. A gap means the sampling window or the PID-namespace assumption is
   wrong.
2. Σ working set ≤ node `MemAvailable` delta at all times. A crossing means the run is in a
   thrash regime, which is a finding.
3. `throttled_usec > 0` for a session ⇒ exclude its latency from a density claim, or re-run with
   limits removed. Without this, "9 active browsers per host" silently becomes "9 processes
   shaped like browsers at 500m each".

## 7. What changes when the numbers exist

Two numbers move conclusions further than the rest of the row:

- `nr_throttled` / `throttled_usec` — decides whether a density run measured density or the
  template's own CPU limits.
- `workingset_refault_*` — decides whether swap buys capacity or buys latency
  (`03-browser-density.md:389`).

The cost is roughly fifty lines in the harness plus one node-exporter collector flag. The
alternative, a BPF program, adds the measurement-instrumentation problem to a run that is trying
to measure something else.

## 8. How to check this note

```bash
# cgroup v2 with PSI available
test -f /proc/pressure/cpu && echo psi-present

# fields the note names, on a live sandbox pod cgroup
sudo awk '/^(usage_usec|nr_throttled|throttled_usec)/' \
  /sys/fs/cgroup/kubepods.slice/kubepods-*.slice/cri-containerd-*.scope/cpu.stat
sudo awk '/^(anon|file|shmem|workingset_refault_anon|workingset_refault_file)/' \
  /sys/fs/cgroup/kubepods.slice/kubepods-*.slice/cri-containerd-*.scope/memory.stat

# the fork's density parser, which reads the host PSI families this note adds to node-exporter
rg -n "psi_waiting_seconds_total" \
  ~/Documentos/git_repos/agent-sandbox/examples/gke-swap/python-density/parse_telemetry.py
```
