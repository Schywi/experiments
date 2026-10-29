# Session lifecycle elasticity — Phase 8 design notes

| | |
|---|---|
| Status | Draft for review |
| Date | 2026-10-29 |
| Phase | 8 of this repository's evolution (6 = Kargo promotion, 7 = agent-sandbox and the console) |
| Branch | `research/session-lifecycle-elasticity`, cut from `cluster-evolution` at `078d1b9` |
| Scope | Designing a test for heterogeneous session lengths, exclusive holds, waiting agents and an elastic warm pool. Design notes only; no cluster manifest changes |
| Subject | `kubernetes-sigs/agent-sandbox`, read through the fork checkout `~/Documentos/git_repos/agent-sandbox` |
| Related | [`../rfc-sandbox-console.md`](../rfc-sandbox-console.md) (Phase 7), [`../../ROADMAP.md`](../../ROADMAP.md) |

---

## The three notes

| Note | Question | Summary |
|---|---|---|
| [01 — Measuring CPU and memory per session](01-instrumentation-cpu-memory.md) | What instrument produces a per-session CPU and memory number? | cgroup v2 files plus PSI, read per pod: the field tables, the `working_set ≈ current − inactive_file` accounting trap, three collection paths, and the four questions that justify tracing |
| [02 — Objective and what a 10k-session run can claim](02-objective-and-soak-statistics.md) | What is the experiment, and what may its result say? | Soak / capacity / mechanism split, the Little's-law arithmetic that lands ~110 peak concurrency on ~12 hosts, rule-of-three bounds on a zero-failure result, and the metric gaps |
| [03 — What exists today](03-what-exists-today.md) | What does the platform already do, and what does a session-elasticity test still need? | The five `test/stress` phase kinds, external-only autoscaling via HPA/KEDA, the four meanings of "idle", and five gaps |

Read 03 first for the baseline, 02 for the experiment shape, 01 for the instrument.

## What the three notes conclude

- **CPU and memory need cgroup v2 and PSI, not eBPF.** The fork's own GKE density study was built
  on cAdvisor CSV plus PSI (`examples/gke-swap/python-density/parse_telemetry.py:15`). Tracing is
  justified for four narrower questions: which process inside the sandbox burned the CPU, whether
  the session was blocked off-CPU, which pages refault, and what a "blank" reuse actually costs.
- **The harness measures no per-sandbox CPU or memory today.** Node totals are covered by
  node-exporter (`test/benchmarks/scenarios/benchmarks-kops-gcp/node-exporter.yaml:54-58`), and
  cAdvisor was skipped deliberately (`test/stress/promscrape.go:63-65`). That gap is what forces
  the `0.3 vCPU` assumption at `research/experiments/02-capacity-6vcpu-100k.md:133-134`, which
  every density figure in the fork inherits.
- **Two metrics change the most conclusions:** `nr_throttled` / `throttled_usec` (did the run
  measure density or the template's own 500m CPU limits) and `workingset_refault_*` (does swap buy
  capacity or buy latency).
- **A 20-hour 10k-session run is a correctness experiment** at ~37 mean concurrency. Its output is
  a failure-class taxonomy with a bounded rate: with no failures observed, the claim is
  ≤3×10⁻⁴/session by the rule of three.
- **One code-free change buys per-bucket slicing:** one `SandboxTemplate` and one pool per session
  length, because `sandbox_template` is already a label on the latency histograms and on
  `agent_sandbox_claim_creation_total`.
- **Elasticity today is the pool replicas field plus an external HPA or KEDA object.** No
  queue-depth metric exists, and no test asserts a replica trajectory in response to excess
  demand.

## Provenance of the citations

Every `path:line` in these notes was read on 2026-10-29 in the checkout
`~/Documentos/git_repos/agent-sandbox`, branch `research/devex-cilium-archetype`, commit
`d816e0f`, which is 45 commits ahead of that fork's `origin/main`.

That checkout matters for two reasons:

- The `research/*.md` paths cited here (`research/experiments/02-capacity-6vcpu-100k.md`,
  `research/experiments/03-browser-density.md`, `research/05-cilium-networking.md`,
  `research/06-devex-local-setup.md`, `research/09-your-k3s-playbook.md`) exist only on that
  branch — `origin/main` carries no `research/` tree. They are fork-local design documents.
- The code paths (`api/`, `extensions/`, `internal/`, `test/`, `sandbox-router/`, `docs/`,
  `examples/`) are upstream repository paths.

Six references drifted between the analysis and the checkout, and the notes use the corrected
form:

| Analysis | Reads as in `d816e0f` |
|---|---|
| `test/stress/promscrape.go:177-179` | `:178-183` |
| `test/stress/promscrape.go:198-209` | `:200-212` |
| `research/experiments/02-capacity-6vcpu-100k.md:135` | `:133-134` |
| `research/experiments/02-capacity-6vcpu-100k.md:141` | `:138` |
| `research/experiments/02-capacity-6vcpu-100k.md:186` | `:190` |
| `research/experiments/03-browser-density.md:100-110` | `:106-113` |
| `extensions/controllers/sandboxclaim_controller.go:88-93` | `:88-95` |
| `extensions/controllers/sandboxclaim_controller.go:362-476` | `:362`, `:466-490`, `:493-500` |
| `extensions/controllers/queue/simple_sandbox_queue.go:24-47` | `:28-47` |

## Phase numbering

Phase 6 is Kargo promotion and the assistant evaluator, recorded in
`evolution/06-kargo-promotion-pipeline.md` on `cluster-evolution`. Phase 7 is agent-sandbox and the
sandbox console, recorded in `evolution/07-sandbox-console.md` on `feat/sandbox-console`
(renumbered from 06 in `dd9ba54`). Phase 8 is this work.

`evolution/README.md`'s phases table stops at Phase 5 on every branch and needs rows for 6, 7 and
8; that edit belongs with the Phase 8 narrative, not with these design notes.

## Open next steps

1. Write a per-session cgroup sampler for `test/stress` (note 01 §3-§6) and the outcome enum of
   note 02 §7, then run the four-bucket template layout of note 02 §6 as the first A-shaped run.
2. Decide whether the elasticity assertion of note 03 §6.4 belongs in `test/stress` as a new phase
   kind or in a ClusterLoader2 recipe, and add the waiting-claims gauge either way.
3. Land the Phase 8 narrative in `evolution/08-*` once the first run has anchors to cite.

## How to check these notes

```bash
# the fork checkout and commit the citations were read at
git -C ~/Documentos/git_repos/agent-sandbox log --oneline -1 d816e0f

# three of the corrected references
rg -n "Not scraped \(deliberately\)" ~/Documentos/git_repos/agent-sandbox/test/stress/promscrape.go
rg -n "\*\*9 active browsers\*\*" ~/Documentos/git_repos/agent-sandbox/research/experiments/02-capacity-6vcpu-100k.md
rg -n "terminates the backing Pod while retaining" ~/Documentos/git_repos/agent-sandbox/api/v1beta1/sandbox_types.go
```
