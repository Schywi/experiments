# Session lifecycle elasticity — Phase 8 design notes

| | |
|---|---|
| Status | Draft for review |
| Date | 2026-10-29 |
| Phase | 8 of this repository's evolution (6 = Kargo promotion, 7 = agent-sandbox and the console) |
| Branch | `research/session-lifecycle-elasticity`, cut from `cluster-evolution` at `078d1b9` |
| Scope | A Go session scheduler and experiment runner at `apps/session-lab/`, with design notes here |
| Subject | `kubernetes-sigs/agent-sandbox`, read through the fork checkout `~/Documentos/git_repos/agent-sandbox` |
| Related | [`../rfc-sandbox-console.md`](../rfc-sandbox-console.md) (Phase 7), [`../../ROADMAP.md`](../../ROADMAP.md) |

---

## The notes

| Note | Question | Summary |
|---|---|---|
| [01 — Measuring CPU and memory per session](01-instrumentation-cpu-memory.md) | What instrument produces a per-session CPU and memory number? | cgroup v2 files plus PSI, read per pod: the field tables, the `working_set ≈ current − inactive_file` accounting trap, three collection paths, and the four questions that justify tracing |
| [02 — Objective and what a 10k-session run can claim](02-objective-and-soak-statistics.md) | What is the experiment, and what may its result say? | Soak / capacity / mechanism split, the Little's-law arithmetic that turns a target rate into a host count, rule-of-three bounds on a zero-failure result, and the metric gaps |
| [03 — What exists today](03-what-exists-today.md) | What does the platform already do, and what does a session-elasticity test still need? | The five `test/stress` phase kinds, external-only autoscaling via HPA/KEDA, the four meanings of "idle", and five gaps |
| [04 — The deliverable](04-deliverable.md) | What gets built, and under which rules? | The session scheduler and experiment runner: four parts, the lifecycle table, the outcome enum, the four policies, the three elasticity configurations, the report contract, and the boundary list |
| [05 — The first run](05-first-run.md) | What did the runner actually measure, and what did it get wrong? | A two-session smoke test with its numbers, the three defects execution found, and the throttling that makes its hold latency an artifact |
| [06 — Audit](06-audit.md) | Planned vs. built, area by area | Nine areas with implemented / live / verified / gap columns, and the fix order: metrics endpoint, scrape job, template limits, dashboard, overload run, suspend test |

Read 04 for what is being built and under which rules, 03 for the baseline, 02 for the experiment
shape, 01 for the instrument.

## The rule about numbers

**The runner imposes no ceiling, and no figure from a document is an input to it.** Concurrency,
session count, pool size and worker count are whatever the operator asks for; node capacity, pool
status and template shape are read from the API at run time. The notes below still quote numbers
that came from upstream documentation, from the fork's research, or from Phase 7's write-up. Each
note marks them as unverified in its own "Status of the numbers" block. A run of
`apps/session-lab/` replaces them, and the replacement is recorded with the run that produced it.

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

1. Build `apps/session-lab/` to the spec in [04](04-deliverable.md): the SQLite session store, the
   Kubernetes adapter, the bucketed scheduler, the measurement pass and the report.
2. Run the four policies at increasing load until the platform refuses, and record which constraint
   broke first.
3. Write `evolution/08-*` once the first run has anchors to cite, and add the missing rows for
   phases 6, 7 and 8 to `evolution/README.md` in that same commit.

## How to check these notes

```bash
# the fork checkout and commit the citations were read at
git -C ~/Documentos/git_repos/agent-sandbox log --oneline -1 d816e0f

# three of the corrected references
rg -n "Not scraped \(deliberately\)" ~/Documentos/git_repos/agent-sandbox/test/stress/promscrape.go
rg -n "\*\*9 active browsers\*\*" ~/Documentos/git_repos/agent-sandbox/research/experiments/02-capacity-6vcpu-100k.md
rg -n "terminates the backing Pod while retaining" ~/Documentos/git_repos/agent-sandbox/api/v1beta1/sandbox_types.go
```
