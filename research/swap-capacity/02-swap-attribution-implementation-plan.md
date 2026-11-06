---
title: 'Implementation plan: swap attribution across workload class and pool'
kind: design
state: hypothesis
date: 2026-11-05
branch: 'docs-site'
commits: [4285593]
tags: [swap, attribution, session-lifecycle, state-hypothesis]
---

# Implementation plan: swap attribution across workload class and pool

[Note 01](01-attribution-across-classes.md) records the contract a run has to
satisfy before an attribution claim is admissible, and which of its five items
exist. This note is the build order for the three that do not: the tier carrier,
the tier recorded per instance, and the report grouping.

| | |
|---|---|
| Status | Planned, nothing built |
| Date | 2026-11-05 |
| Phase | session lifecycle |
| Branch | `docs-site` |
| Related | [attribution contract](01-attribution-across-classes.md), [swap as capacity](README.md), [pool split](../session-lifecycle-elasticity/20-cpu-gpu-pool-split.md) |

## Scope

Three links of the chain in note 01 are ours to build:

```
tier annotation on the SandboxTemplate podTemplate      config/sandbox chart
tier recorded on the instance row                       apps/session-lab
report grouped by (class, pool, tier)                   apps/session-lab
```

Two links are not:

```
the cgroup write that turns an allowance into memory.swap.max
  owned by the swap experiment; this plan writes the string only and reads it back
the GPU SandboxTemplate itself
  applied to the cluster outside this checkout; documented here, never edited here
```

## Assumptions

| Assumption | Value | Why it is fixed here |
|---|---|---|
| Annotation key | `session-lab.io/spill` | matches the existing `session-lab.io/*` idiom (`internal/infra/sandbox/sandbox.go:70-76`) |
| Vocabulary | `none`, `zram-2to1`, `zram-5to1`, `disk` | the tiers the swap study measures (README §5d.1) |
| CPU template tier | `zram-2to1` | the capacity winner of the ratio run |
| GPU template | applied outside this checkout, carries the same key by hand | it is not a chart object here, so this plan documents it and never edits it |
| Writer | owned by the swap experiment | our side writes the string, reads it back, and records it |
| Grouping | computed in Go from `st.Sessions()` + `st.Instances()` | no new SQL join, no column on `sessions` |

A pod whose template carries no annotation reads as tier `-` in every
grouping. That is deliberate: an untiered pod is a visible gap in the table
rather than a silently merged row.

## Phase 1 — the CPU template carries the tier annotation

GPU template untouched.

- `config/sandbox/values.yaml`: `spill: zram-2to1` under `sandboxes.template`,
  commented with the annotation key it renders and who reads it.
- `config/sandbox/templates/sandboxtemplate.yaml`: under `podTemplate.metadata`,
  guard-render `annotations: {session-lab.io/spill: <value>}` with
  `{{- with .Values.sandboxes.template.spill }}` so an empty value renders no
  annotation at all. The existing `labels` stay as they are.
- The note records that the GPU template's `podTemplate.metadata.annotations`
  has to carry the same key, and that a pod without it groups as tier `-`.

Verify:

```bash
helm template config/sandbox --show-only templates/sandboxtemplate.yaml | grep -A2 annotations
bash config/ci/validate.sh
```

## Phase 2 — session-lab records the tier per instance

- `internal/infra/sandbox/sandbox.go`: `SpillTier string` on `TemplateLimits`
  and on `Instance`.
- `internal/infra/kube/kube.go` `Template()`: read
  `spec.podTemplate.metadata.annotations.session-lab.io/spill` with
  `unstructured.NestedString`, assign it to `limits.SpillTier`. Error paths
  unchanged.
- `internal/runtime/session/run.go` preflight: build `map[pool]tier` over
  `r.poolNames()`, each entry via `ad.Pool()` for the template name and
  `ad.Template()` for the tier, falling back to `cfg.Template` when the pool
  reports none. A run that draws from two pools then carries two tiers.
- Set `instance.SpillTier` immediately after `ad.Acquire(...)` at both call
  sites, using the pool that call used.
- `internal/platform/model/model.go`: `SwapTier string` on `Instance`; the
  instance row built for `SaveInstance` passes it through.
- `internal/platform/store/store.go`: `swap_tier TEXT NOT NULL DEFAULT ''` in
  the `instances` table, an ALTER migration for `instances` (the existing loop
  covers `sessions` only), plus `SaveInstance` and `Instances()`.

Verify:

```bash
cd apps/session-lab && go test ./...
```

A `--dry-run` writes one instance row whose `swap_tier` matches the template
annotation.

## Phase 3 — the report groups by (class, pool, tier)

- `internal/runtime/report/report.go`: type
  `Group{Class, Pool, Tier, Sessions, Swapped, SwapP95MiB, CPUCoreSecondsP50, QueueP95Ms, Failed}`
  and `Groups []Group` on `Report`.
- In `Build()`: index `st.Instances()` by id, bucket sessions on
  `(instance.class, session.Pool, instance.swap_tier)`, reusing the existing
  percentile helpers. A session whose instance row is missing lands in tier `-`.
- In `Print()`: the table below, printed after the resources block.

```
class   pool        tier        sess  swapped  swap p95  cpu core-s  queue p95  fail
normal  browser-pool zram-2to1      8      8/8    61 MiB       4.21       97 ms     0
```

- `Failed` counts outcomes other than success, so a group can read "held but
  slow" next to "held and fast".
- `internal/runtime/report/report_swap_test.go`: two-group fixture, one tiered
  instance and one untiered, asserting both rows, the swapped counts and the
  tier `-` bucket.

Verify:

```bash
cd apps/session-lab && go test ./... -run Report
cd apps/session-lab && go test ./...
```

## Phase 4 — close out the contract note

- `research/swap-capacity/01-attribution-across-classes.md`: contract items 1
  and 4 move to implemented with `file:line` citations. Items 2 (recorded
  allowance bytes) and 5 (verdict wording) stay marked as the swap experiment's
  side. The GPU template is recorded as applied outside this checkout.
- The same note gains the arm list: CPU pool at `zram-2to1`, GPU pool at
  `zram-2to1` set by hand, with the commands written and not run.

## Blast radius

```
phase  files  runtime effect                              if wrong
1        2    none until helm upgrade; a pod annotation,   stale note only
              inert unless the writer reads it
2        2    none until helm upgrade                       annotation typo -> tier "", no other harm
3        5    session-lab binary only; additive column,     map keyed by the wrong pool name -> tier "-"
              old run databases still open
4        2    report output only                            an empty group is a visible gap
5        1    nothing                                       stale note
```

`config/swap-lab/`, `config/vm-swap-lab/` and every swap script stay untouched.
No cgroup write happens anywhere in this plan.

## Gates

```bash
bash config/ci/validate.sh
cd apps/session-lab && go test ./...
git diff --check
```

Two commits: phases 1-2 as `feat(session-lab)`, phase 3 as `feat(report)`, each
citing the bd issue.

## Running the arms

Written here so the sequence is reviewable; none of it was executed by this
change. The chart is the only thing an operator applies (helm or Argo CD), and
the swap allowance is set by the swap experiment's writer, which selects pods on
`session-lab.io/spill`.

| Arm | Pool | Class | Tier (from the template) | What it answers |
|---|---|---|---|---|
| A | `browser-pool` | `normal` | `zram-2to1` | CPU cell, tiered, baseline |
| B | `browser-pool-gpu` | `normal` | `zram-2to1` | GPU cell, same tier, same class |
| C | `browser-pool` | `bulk` | `zram-2to1` | same cell as A, lower queue priority |
| D | both pools in one run | `normal` | `zram-2to1` each | the attribution table with two cells at once |

```bash
# apply the chart (browser-tpl, browser-tpl-gpu, browser-pool, browser-pool-gpu)
helm upgrade --install sandbox config/sandbox -n sandbox-demo

# raise the GPU cell for arms B and D (a values change or an Argo sync;
# no kubectl scale: live edits are how a result stops being reproducible)
#   sandboxes.pools[browser-pool-gpu].replicas: 4

# arm A
apps/session-lab/session-lab --sessions 24 --fixed-arrival --rate 1 \
  --pool browser-pool --class normal --contexts-per-browser 1 \
  --out "$OUT/arm-a"
# expect a report row: class=normal pool=browser-pool tier=zram-2to1

# arm B
apps/session-lab/session-lab --sessions 24 --fixed-arrival --rate 1 \
  --pool browser-pool-gpu --class normal --contexts-per-browser 1 \
  --out "$OUT/arm-b"

# arm D: one run, two cells
apps/session-lab/session-lab --sessions 48 --fixed-arrival --rate 1 \
  --pool browser-pool --pool-by-class normal=browser-pool \
  --pool-by-class bulk=browser-pool-gpu --class bulk --contexts-per-browser 1 \
  --out "$OUT/arm-d"
```

The report prints one row per (class, pool, tier). A row reading `tier -` means
the pod's template carried no annotation, and `swapped 0/N` with `swap p95 0 MiB`
means the writer never gave that cell an allowance.
