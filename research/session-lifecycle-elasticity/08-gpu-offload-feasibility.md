# Note 08 — Can the RX 570 reduce session CPU cost, and is KubeVirt passthrough the way?

| | |
|---|---|
| Status | Research note, no implementation |
| Date | 2026-10-29 |
| Phase | 8 — session lifecycle elasticity |
| Branch | `research/session-lifecycle-elasticity` |
| Scope | Read-only audit of this repository, the live cluster, the host, and the `agent-sandbox` fork checkout |
| Related | [05](05-first-run.md), [06](06-audit.md), [07](07-overload-and-suspend.md), `../../config/gpu/README.md`, `../../apps/gpu-device-plugin/` |

Labels used throughout: **implemented** / **configured** / **deployed** / **verified live** /
**measured** / **inferred** / **unknown**. Documentation alone never carries a *verified live*
label. Where a figure comes from a note or a doc rather than a run in this session, it is marked
**measured** (a run recorded it) and the note is named.

---

## 1. Verdict

**KubeVirt GPU passthrough: infeasible on this host as currently booted, and the wrong instrument
for the question even if it worked.**

Three independent blockers, each sufficient on its own:

1. **No IOMMU.** `/sys/kernel/iommu_groups` is empty (0 entries) and `/proc/cmdline` carries no
   `intel_iommu=on` (**verified live**). `vfio-pci` has nothing to bind, so PCI passthrough cannot
   be configured without editing the kernel command line and rebooting the host.
2. **One GPU, used by the desktop.** The RX 570 is bound to `amdgpu` and drives this workstation's
   display; the host CPU is an Intel i5-9400F with no integrated GPU to fall back on (**verified
   live**). Assigning the card to a guest removes the host's display until the binding is undone
   and the machine reboots — the exact action `AGENTS.md` forbids agents from taking.
3. **Exclusive assignment cannot raise session concurrency.** The plugin advertises one device
   (`amd.com/render: 1`) and passthrough gives the whole card to one VM (**verified live**,
   `apps/gpu-device-plugin/main.go:47-58`). One GPU-backed session at a time, on a node that
   currently sustains ~18 concurrent sessions, changes nothing about sustainable capacity.

**The underlying hypothesis — that CPU is the limiting resource — is partially supported and
partly unproven.** What the runs actually establish is narrower than the hypothesis: admission
control refused on CPU *requests* before anything else, and every sampled session was CFS-throttled
at the template's 1000m limit. Neither of those is the same claim as "the browser workload needs
more CPU than the node has".

**The experiment worth running is the container path with acceleration actually enabled.** The
shipped image runs Chromium with `--disable-gpu` and Vulkan disabled (**implemented**,
`apps/browser-sandbox/entrypoint.sh:43-58`), so every pixel today is produced by the CPU. Turning
that on is a one-flag, one-device change with no reboot, no kernel change, and no desktop
disruption, and it answers the same question passthrough would answer, minus months of host
surgery.

---

## 2. Evidence base

Read in this session:

| Source | What it establishes |
|---|---|
| `research/session-lifecycle-elasticity/05-first-run.md`, `07-overload-and-suspend.md` | The only two runs with recorded numbers (**measured**) |
| `config/sandbox/values.yaml:73-81` | Template requests 100m/256Mi, limits 1000m/768Mi (**configured**) |
| `config/sandbox/templates/sandboxtemplate.yaml:39-57` | Readiness is `/json/version`; `/dev/shm` is a memory emptyDir (**configured**) |
| `apps/browser-sandbox/entrypoint.sh:43-58` | Chromium flags, including `--disable-gpu` (**implemented**) |
| `apps/gpu-device-plugin/main.go:34-102` | Glob discovery, device cgroup grant, no capacity model (**implemented**) |
| `config/gpu/README.md:55-87` | Device identity, and why the plugin exists (**measured**, on this host, earlier) |
| `benchmark/experiments/e3-amd-gpu/RESULTS.md` | Vulkan works from a Pod; CPU fallback is silent (**measured**) |
| `benchmark/experiments/e3-amd-gpu/manifests/gpu-sharing-probe.yaml` | Two pods cannot both hold the device (**configured**, by design) |
| `config/victoriametrics/values.yaml:27-35`, `templates/configmap-scrape.yaml:73-98` | What is scraped, and what is not (**configured**) |
| `config/grafana/README.md:26-45` | The ten session-lab panels (**configured**/**deployed**) |
| `apps/sandbox-console/cdp.py:258-295` | Real console traffic: screenshot, click, scroll (**implemented**) |
| Fork `research/experiments/02-capacity-6vcpu-100k.md`, `03-browser-density.md` | Upstream density reasoning and one reference measurement (**measured** elsewhere) |

Live and read-only, this session (**verified live**):

```
node allocatable:  cpu 6, memory 16305712Ki, pods 110, amd.com/render 0
gpu-device-plugin log: "advertising 1 render node(s) [renderD128]" / "registered amd.com/render with kubelet"
amd.com/render capacity/allocatable: 0 / 0
dmesg/CPU: GenuineIntel i5-9400F, vmx, kvm_intel loaded
IOMMU: /sys/kernel/iommu_groups -> 0 entries, /sys/class/iommu -> empty
GPU PCI: 0000:01:00.0 (0x030000 VGA), 0000:01:00.1 (0x040300 audio), amdgpu bound
amdgpu sysfs: gpu_busy_percent 0, mem_info_vram_used 404549632, mem_info_vram_total 4294967296
DaemonSets: cilium, cilium-envoy, gpu-device-plugin. No node-exporter, no cAdvisor, no GPU exporter
metrics-server: v1beta1.metrics.k8s.io Available (node CPU 1104m/18%, memory 5719Mi/35%)
KubeVirt: absent (no kubevirt/virtualmachine API kinds)
```

Note the contradiction: the plugin logs a successful registration and the DaemonSet is 1/1 Ready,
while the node advertises `amd.com/render` **0**. A leftover `llm-gpu` pod sits in
`UnexpectedAdmissionError`. Whatever the cause, the GPU is not schedulable right now, so any
GPU experiment starts by repairing that.

---

## 3. Bottleneck analysis

### 3.1 What the runs establish (**measured**)

The overload run (`07`, 60 sessions, offered concurrency 60, pool 20, policy `reuse`):

| Quantity | Value |
|---|---|
| outcomes | 42 `success`, 17 `acquisition_failed`, 1 `reset_failed` |
| first refusal | `0/1 nodes are available: 1 Insufficient cpu` |
| measured concurrency | peak 18 against an offered 60 |
| queue wait | p50 3.2 s, p95 134 s, p99 136 s |
| CPU per session | 1.77 core-seconds p50, 3.88 max, 93.96 total |
| throttled | 43 of 43 sampled |
| memory per session | 154 MiB p50 peak, 209 MiB max, 168 MiB working-set max |
| node memory | MemAvailable floor 3322 MiB of 15924 MiB (20.9%), swap 4418 MiB |
| other boundary | one `502` from the CDP bridge at 18 live browsers |

The suspend run (`07`): 10 sessions, resume p50 2853 ms against warm adoption at 107 ms, 10 of 10
throttled, node memory floor 48.2%.

The smoke run (`05`): 2 sessions, 1.19 core-seconds p50 over a 5 s hold, **2 of 2 throttled**.

### 3.2 The admission refusal is an accounting artifact

60 pods × 100m request = 6000m, which is exactly the node's allocatable CPU (**inferred**, arithmetic
from `values.yaml:74-75` and the live allocatable). The run offered exactly the load that consumes
the whole request budget, so the scheduler refused. Moving the request to 250m would refuse at 24
pods while the CPU actually consumed stays where it is. This is a real operational limit and a real
thing to tune, but it is not evidence about what the browser needs.

### 3.3 Throttling is universal and does not prove saturation

Every sampled session in all three runs was throttled at the 1000m limit. At the same time the CPU
reported per session is small: 1.77 core-seconds over a 30 s hold is an average of 0.06 cores
(**inferred**). A cgroup throttles when a burst consumes its whole quota inside one 100 ms period,
so universal throttling with low average usage describes **short, sharp bursts** — process spawn and
first paint — not sustained demand.

That reading matters for the hypothesis. The bursts are exactly where software rasterization lands,
so a GPU path could plausibly reduce them. It is also why the current runs cannot be used to
estimate capacity: the report itself says hold latency carries the limit.

### 3.4 Memory is the next wall, and it is measured

MemAvailable fell to 20.9% with 4.4 GiB in swap, and 40 of 43 sessions recorded workingset
refaults. Memory did not cause a failure in that run. On a 4 GiB-usable-VRAM card, a GPU path does
not move this wall: Chromium's renderer and browser processes stay in host RAM regardless of who
rasterizes.

### 3.5 The workload is not representative of sandbox use

This is the largest gap between the hypothesis and the evidence. `session-lab`'s hold is a sleep
(**implemented**, `apps/session-lab/internal/run/run.go:485-521`): it samples cgroups on a ticker and
does nothing else. The only browser work any recorded run priced is the entrypoint's initial
`TARGET_URL` load and the blank-page reset.

Actual console traffic is different (**implemented**, `apps/sandbox-console/cdp.py:258-330`):
`Page.captureScreenshot` with `fromSurface: true`, `Input.dispatchMouseEvent`, wheel events, and
`Runtime.evaluate` probes, polled per tile. None of that appears in any session-lab run.

A GPU experiment run through session-lab unmodified would measure cold start and idle rendering,
then generalise about rendering throughput. That is the mistake to avoid.

### 3.6 Instrument gaps that limit what can be concluded

1. **Per-session CPU is not differenced, despite the code comment.** `run.go:46-47` and `run.go:524`
   state that cumulative counters are differenced per session. `applyReading`
   (`run.go:554-574`) assigns `reading.CPUUsec` and `reading.ThrottledUsec` directly, and `hold()`
   returns the last cumulative sample. On a reused instance, session *n* carries the CPU and
   throttled time of sessions 1..*n*. The per-session CPU figures, the "total" column, and the
   "throttled sessions" count are therefore inflated on warm instances. This is a defect against
   the tool's own contract, and it must be fixed before either run is used as a CPU baseline.
2. **No node-level CPU series.** The runner samples the *pod* cgroup and host memory; it does not
   record node CPU utilisation or host-wide PSI. So "the node was CPU-saturated" cannot be checked
   from a run's own rows.
3. **No GPU signal anywhere.** No exporter, no per-process GPU counter, no VRAM series.
4. **`gpu_busy_percent` is a weak instrument on its own.** The E3 results already recorded it
   reading 0 between requests (**measured**), and it read 0 during this audit.

---

## 4. GPU audit

| Property | Value | Label |
|---|---|---|
| Model | AMD Radeon RX 570 Series (RADV POLARIS10), 8 GB card | **measured** (`config/gpu/README.md:59-65`) |
| PCI IDs | vendor `0x1002`, device `0x67df`; BDF `0000:01:00.0` VGA + `0000:01:00.1` audio | **verified live** |
| Host driver | `amdgpu`, bound to `01:00.0`; 33 references (desktop in use) | **verified live** |
| Vulkan | Mesa RADV, Mesa 26.2.3 | **measured** |
| Compute path | Vulkan only; `gfx803` is off AMD's ROCm list, `/dev/kfd` must not be mounted | **configured** (`config/gpu/README.md:14-26`) |
| Resource name | `amd.com/render`, one unit | **implemented** |
| Currently schedulable | no: capacity 0 / allocatable 0 | **verified live** |
| VRAM visible to the host | 4294967296 (~4 GiB total), 404549632 (~386 MiB) in use by the desktop | **verified live** |
| IOMMU | disabled: 0 groups, no `intel_iommu=on` | **verified live** |
| KVM | `kvm_intel` loaded; hardware virt available | **verified live** |
| Sharing | exclusive by design: one device, one pod; a second pod stays Pending | **configured**, `gpu-sharing-probe.yaml` |
| Mediated devices / SR-IOV / MIG | none on Polaris; not exposed by amdgpu | **inferred** from hardware generation, not tested |
| KubeVirt | not installed | **verified live** |

### Coexistence with a passthrough arm

The plugin and passthrough are mutually exclusive, not merely awkward together. `vfio-pci` binding
the VGA function removes it from `amdgpu`, `discoverRenderNodes` globs `/dev/dri/renderD*`
(`main.go:47-58`), and with no render node the plugin exits at startup
(`main.go:118-125`, `log.Fatalf`). A passthrough experiment therefore requires uninstalling or
disabling `gpu-device-plugin` on that node first. It also cannot coexist with `gpu-exp/llm-gpu`,
which requests `amd.com/render: 1` and holds the single device.

Sharing is worth stating plainly: the plugin's one-unit advertising means **one pod**, and
passthrough means **one VM**. Neither offers partitioning. Multiple containers mounting the same
`renderD128` would share the card at the Mesa level, which is untested here (**unknown**) and is not
what the plugin currently permits.

---

## 5. Offload map

| Component | GPU-eligible | How to verify it moved | Expected direction |
|---|---|---|---|
| Compositing and rasterization (today SwiftShader on the CPU) | yes, plausible | CDP `SystemInfo.getInfo` `featureStatus`, plus `gpu_busy_percent` during a capture loop | CPU per session down on first paint; direction of *capacity* impact **unknown** |
| `Page.captureScreenshot` readback and PNG encode | partly (readback yes; PNG encode is CPU) | compare screenshot p95 and CPU delta across arms | latency possibly down; CPU savings small |
| Video decode (VA-API) | yes on Polaris, if the page has video | VA-API status in `SystemInfo`, decode counters | **unknown**; no recorded run has video |
| WebGL / WebGPU | yes in principle | `featureStatus.webgl`, context creation succeeds | **unknown**; no workload uses it |
| JS execution, DOM, CSS style and layout | no | — | unchanged by any GPU arm |
| Navigation, networking, DNS | no | — | unchanged |
| CDP bridge (Python HTTP shim) and the router | no | — | unchanged; the 18-browser `502` is here |
| Session scheduling, claim/ready, Kubernetes control plane | no | — | unchanged; the first refusal is here |
| VM entry/exit and guest boot (KubeVirt arm only) | n/a | measure guest boot to CDP-ready | adds cost; compare against 107 ms warm adopt / 2853 ms resume / ~40 s cold boot |

The upstream fork hypothesised that removing the software rasterizer returns roughly one core and
~150 MiB per session, based on a reference measurement where the gpu-process held 164 MiB of
`Pss_Anon` and about one core on a static page (**measured elsewhere**, `03-browser-density.md`
§4.5). That is a hypothesis for *this* host, not a saving to quote: this host's runs never
isolated it, and the 0.3 vCPU-per-browser constant the fork's density tables rest on is explicitly
an assumption (**unknown**).

---

## 6. Option comparison

| | A. Container, GPU access as shipped | B. Container, acceleration enabled | C. KubeVirt + passthrough | D. CPU-only baseline (limit raised) |
|---|---|---|---|---|
| What it is | current image: device grantable, `--disable-gpu`, Vulkan off | same image, flags changed, `amd.com/render: 1` | VM with the whole card | current image, template CPU limit raised |
| Feasibility today | works, GPU unused | needs `amd.com/render` > 0 (currently 0) and flag/pod-spec change | **blocked**: no IOMMU, no KubeVirt, one GPU on the desktop | works today |
| Isolation | pod + NetworkPolicy, `runc` shared kernel, Chromium sandbox off | same | strongest guest boundary | same as A |
| CPU savings | none by construction | the thing to measure; direction plausible, size unknown | would inherit B's savings *inside* the guest | none |
| Memory overhead per session | ~140–154 MiB measured; +164 MiB gpu-process class cost when software rasterizing | delta unknown, plausibly down | guest RAM + QEMU + kernel per VM | unchanged |
| Startup latency | ~40 s cold, 107 ms warm adopt, 2853 ms resume (**measured**) | unknown; likely similar or slightly worse if driver init is slower | guest boot added; likely worst of the four | unchanged |
| GPU sharing | n/a | one pod holds the device; sharing at Mesa level untested | one VM, exclusive | n/a |
| Scheduling | no GPU resource needed today | pod becomes unschedulable whenever `amd.com/render` is 0 | node must be VFIO-prepared; a second VM cannot be placed | none |
| Operational complexity | lowest | low: one pod spec, one flag set | highest: kernel cmdline, reboot, `vfio-pci` ids, KubeVirt CRDs/CDI, desktop loss, manual rollback | lowest |
| Effect on sustainable concurrency | none | bounded by 1 device unless sharing is proven; the useful claim is CPU-per-session, not sessions-per-node | negative (one accelerated session, more overhead) | likely positive, and testable immediately |

The decisive point for C: sustainable concurrency on this node is ~18 measured, and passthrough
adds one accelerated session while adding a guest per session. Even a large per-session CPU win
inside the guest does not move a concurrency number that is bounded above by one card.

---

## 7. Experiment matrix

Sequence matters. Each phase gates the next; no phase starts before its predecessor's criterion is
met.

### Phase 0 — repair and verify acceleration (no host change)

| | |
|---|---|
| Question | Does Chromium in this image use RADV when allowed, and is it cheaper in CPU than SwiftShader? |
| Setup | one pod, current image, `amd.com/render: 1`, flags `--use-gl=angle --use-angle=vulkan --enable-features=Vulkan` (exact flag set to be confirmed from `chrome://gpu`) |
| Controls | same pod spec and same page with `--disable-gpu`; sequential, not concurrent; no `llm-gpu` running |
| Measure | CDP `SystemInfo.getInfo` `featureStatus`; `gpu_busy_percent` sampled during a fixed capture loop; `mem_info_vram_used` delta; pod cgroup `cpu.stat` delta over a fixed 60 s capture loop; screenshot p50/p95 |
| Pass | hardware rasterization or compositing reported by `featureStatus`, `gpu_busy_percent` > 0 during capture, VRAM delta > 0 |

### Phase 1 — single session, A/B, warm

| | |
|---|---|
| Question | At concurrency 1, does the accelerated path reduce CPU per session without hurting latency? |
| Setup | identical page, viewport 1280×600, fixed screenshot cadence (e.g. 1/s for 60 s), identical template limits (1000m/768Mi), N=10 sessions per arm, interleaved A/B/A to catch drift |
| Arms | A (shipped, SwiftShader), B (accelerated), D (CPU-only with limit raised to 2000m, to separate the throttling artifact from workload cost) |
| Controls | same image digest, same warm pool, same page bytes, same cadence; one arm at a time; record template limits in the run row |
| Measure | CPU core-seconds per session, `nr_throttled`/`throttled_usec` per session, screenshot p50/p95, memory working set, CDP errors |
| Pass | ≥25% CPU reduction with screenshot p95 no worse than baseline + 10% |

### Phase 2 — concurrency ladder (only if Phase 1 passes)

| | |
|---|---|
| Question | Does the CPU reduction move the first boundary? |
| Setup | fixed sessions, offered concurrency ladder 5 → 10 → 18 → 24 → 30, 3 repeats per rung, pool sized from the node's own report, `--fixed-arrival` for comparability |
| Measure | completed/failed per second, max sustainable concurrency at ≤1% failures, queue wait p50/p95/p99, throttled sessions, node MemAvailable floor, OOM kills, boundary kinds |
| Pass | maximum sustainable concurrency improves by at least one session at equal failure rate |

### Phase 3 — KubeVirt (deferred, user-authorized only)

Blocked by IOMMU, absent KubeVirt, and the single-GPU/desktop conflict. Not reachable by an agent
under `AGENTS.md`. Listed for completeness: its arms would be B versus C at concurrency 1 only,
because C cannot exceed one accelerated session.

### Cleanup and rollback per phase

Delete test pods, restore the warm pool to its pre-run replica count, confirm
`amd.com/render` returns to its pre-run value, and leave no template change behind. Phase 3's
rollback is a kernel command line edit plus reboot, which is why it needs the user.

---

## 8. Metric sources and their limits

| Metric | Source | Limit |
|---|---|---|
| Sessions/s, outcomes | `session_lab_sessions_total` (`internal/metrics/metrics.go`) | only while a run is in progress; static scrape target |
| Concurrency, queue wait, acquire | `session_lab_*` histograms | label set is `policy`, `bucket`, `launch_type` |
| CPU, throttling, PSI per session | cgroup v2 on the host via `internal/measure` | **cumulative, not differenced** (§3.6); pod-scope only |
| Working set | `memory.current − inactive_file` | the accounting trap in note 01 still applies |
| Node CPU | `kubectl top` / metrics-server | 1-minute granularity, no PSI, no throttling |
| Node memory floor | `/proc/meminfo` sampled by the runner | already recorded per run |
| GPU busy, VRAM | `/sys/class/drm/card1/device/gpu_busy_percent`, `mem_info_vram_used` | no exporter; `gpu_busy_percent` reads 0 between requests |
| Acceleration actually engaged | CDP `SystemInfo.getInfo` `featureStatus` | new; not wired into the runner |
| Rendering correctness | screenshot bytes / perceptual hash | needs a deterministic page to be meaningful |

---

## 9. Observability plan, minimum

Prefer what exists. VictoriaMetrics already scrapes the runner from a static target and serves the
ten-panel dashboard; `featureStatus` and the GPU counters are what is missing.

Add, and nothing more:

1. **Per-session counter deltas** in `apps/session-lab` (fixes §3.6 item 1). This is a correctness
   fix, not a feature, and it gates every CPU claim in this note.
2. **A `drive` phase** in the runner that issues a fixed number of screenshots and input events
   during the hold, exported as `session_lab_drive_*`, so the measured workload is the console's
   workload.
3. **One GPU sample per session** read from `/sys/class/drm/card1/device/` at hold start and end
   (`gpu_busy_percent`, `mem_info_vram_used`), recorded in the run row. A node-level exporter can
   wait until Phase 2 shows the numbers matter.
4. **`SystemInfo.getInfo` feature status** captured once per run and stored verbatim in the report,
   so no result can be read without knowing whether acceleration engaged.

Dashboards: two panels added to `session-lab.json` — GPU busy and VRAM alongside the existing
per-session CPU/throttle panel, and a "acceleration" text panel holding the `featureStatus` JSON.
Keep per-session panels separate from node panels, as the existing dashboard already does.

Record with every run: image digest, Chromium flags, template requests and limits, page URL, capture
cadence, warm-pool size, other GPU consumers, and the `featureStatus` blob.

---

## 10. Acceptance criteria

A GPU path counts as useful only if all of these hold, on this host, in this session's runs:

1. **Acceleration is proven engaged.** CDP `featureStatus` reports hardware for compositing or
   rasterization, `gpu_busy_percent` > 0 during capture, and a VRAM delta is observed. No criterion
   below counts if this fails — Vulkan availability is not evidence of Chromium using it.
2. **CPU per completed session falls ≥25%** at identical page, viewport, cadence and template
   limits, measured with per-session differenced counters (§9 item 1), N=10, interleaved A/B/A.
3. **No latency regression**: screenshot p95 ≤ baseline p95 + 10%, and queue wait p95 unchanged.
4. **Throttling falls**: `throttled_usec` per session reduced to zero for ≥95% of sessions at the
   same 1000m limit.
5. **No correctness regression**: zero OOM kills, zero renderer crashes, zero new `reset_failed`,
   and a screenshot hash within threshold of baseline on a deterministic page.
6. **Capacity is claimed only from Phase 2**: max sustainable concurrency at ≤1% failures improves
   by at least one session. Nothing in Phase 0 or 1 may be written up as a capacity number.

If criterion 1 holds and 2 fails, the honest conclusion is that software rasterization is not the
CPU cost on this workload, and the GPU is not the lever. That is a useful result and it costs one
afternoon.

---

## 11. Risks and rollback

| Risk | Consequence | Mitigation / rollback |
|---|---|---|
| GPU exclusivity | `llm-gpu` and the browser cannot both hold `amd.com/render` | stop `llm-gpu` for the duration; restore after |
| Device already unschedulable | Phase 0 fails for reasons unrelated to the hypothesis | repair `amd.com/render` first; confirm 1/1 before measuring |
| Desktop disruption (Phase 3) | `vfio-pci` binding removes `amdgpu` from the display; no iGPU to fall back on | **user-authorized only**; undo binding and reboot |
| Kernel command line change (Phase 3) | `intel_iommu=on` + reboot; a bad edit can leave the host unbootable | keep a prior kernel entry; edit with `grubby`/`grub2-mkconfig` deliberately |
| `AGENTS.md` prohibition | agents must not unbind `amdgpu`, add `vfio-pci` ids, or blacklist GPU drivers | Phase 3 is out of scope for an agent; the container path needs none of it |
| Silent CPU fallback | E3 already shows a Vulkan workload falling back to CPU undetected (`RESULTS.md` §"Spike evidence") | criterion 1 exists precisely for this |
| Cluster availability | a node restart drops `amd.com/render` to 0 and leaves pods `Pending` | verify allocatable before and after every run |
| Template change leaking | a raised CPU limit changes every later run | revert `config/sandbox/values.yaml`, confirm with `kubectl get sandboxtemplate -o yaml` |
| Misreading a throttled run as capacity | the error already present in notes 05 and 07 | Phase 2 gate, per-session differenced counters |

---

## 12. Smallest next step

**Repair `amd.com/render` to 1, then run Phase 0: one GPU-enabled Chromium pod with a fixed
screenshot loop, and the identical pod with `--disable-gpu`.**

That single comparison eliminates the largest unknown in this note — whether Chromium in this image
takes the RADV path at all, and whether that costs less CPU than SwiftShader — while touching no
kernel parameter, no device binding, no cluster configuration, and no desktop session. It also
produces the `featureStatus` evidence that every later claim depends on.

If `featureStatus` shows software rendering even with the flags set, the hypothesis is dead for this
image and no amount of passthrough work will revive it. If it shows hardware rendering and CPU falls,
the container path is the answer and KubeVirt is not needed. Only if the container path cannot be
made to engage the GPU, and the workload is proven to be rasterization-bound, does passthrough
become worth a conversation with the user about the desktop.

## 13. How to check this note

```bash
# the three host facts behind the verdict
ls /sys/kernel/iommu_groups | wc -l          # 0
grep -o 'intel_iommu=[a-z]*' /proc/cmdline   # nothing
lspci -nnk -s 01:00                            # 1002:67df, amdgpu in use

# the live inconsistency Phase 0 must resolve first
kubectl get node fedora -o jsonpath='{.status.allocatable.amd\.com/render}{"\n"}'
kubectl -n gpu logs ds/gpu-device-plugin --tail=5

# the flag that makes option A GPU-free by construction
rg -n 'disable-gpu|Vulkan' apps/browser-sandbox/entrypoint.sh

# what a run does during its hold
sed -n '485,521p' apps/session-lab/internal/run/run.go

# the differencing contract the code does not meet
sed -n '44,48p;524,575p' apps/session-lab/internal/run/run.go

# the GPU numbers nothing scrapes yet
cat /sys/class/drm/card1/device/gpu_busy_percent \
    /sys/class/drm/card1/device/mem_info_vram_used \
    /sys/class/drm/card1/device/mem_info_vram_total
```
