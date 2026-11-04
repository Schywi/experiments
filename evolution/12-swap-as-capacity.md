---
title: 'Phase 12 — Swap as capacity: the zram lab (runs 2026-11-02 → 11-03, recorded 2026-11-04)'
date: 2026-11-04
kind: narrative
phase: 12
tags: [swap, cpu]
---

# Phase 12 — Swap as capacity: the zram lab (runs 2026-11-02 → 11-03, recorded 2026-11-04)

**Branch:** `research/session-lifecycle-elasticity` · **tracked paths:**
[`../research/swap-capacity/`](../research/swap-capacity/README.md),
[`../config/vm-swap-lab/`](../config/vm-swap-lab/README.md),
[`../config/swap-lab/`](../config/swap-lab/up.sh)

Phases 8–11 all bought capacity by rearranging what was already in RAM. This phase
asks the blunter question: can the host trade RAM for **compressed** RAM and get
more sessions out of the same box?

## The two questions

1. **Can swap be used for these workloads at all?** — **Yes [M]**, proven
   mechanically in the lab.
2. **Does it raise concurrent sessions, and by how much?** — **Yes, by one
   session: 5 → 6 (+20 %)**, at 3 vCPU / 4 GiB; and with zram it also cuts tail
   latency **~70 %** at equal load.

Both answers carry the study's own label (`[M]` measured in this lab, `[D]`
documented upstream, `[I]` inferred, `[U]` unknown). The labels are not decoration:
the note keeps a section for each and one for what could not be settled.

## The apparatus

Two labs, because the question needed two different environments:

| Lab | What it is | Why |
|---|---|---|
| `config/vm-swap-lab/` | Disposable QEMU VM, Ubuntu 24.04 then Fedora, k3s `v1.36.5+k3s1`, zram `zram-size = ram/2`, zstd, kubelet `failSwapOn: false` + `memorySwap.swapBehavior: LimitedSwap` | `memorySwap` is a kubelet-wide setting; testing it on the live host would change the host |
| `config/swap-lab/` | Host-side pressure harness: `up.sh`, `down.sh`, `mempressure.py`, cloud-init, ansible | The smaller, repeatable arm for pressure without a VM |

2,629 lines of scripts and ansible in the VM lab; 375 in the host lab. Both were
recorded in the tree on 2026-11-04 with `id_ed25519`, the VM images, the caches
and `kubeconfig` excluded — the labs generate their own credentials.

## The ratio run, and why its verdict is split

The open question at the end of the first pass was *which compression ratio* pays
for the latency win at the lowest CPU cost. The measured answer is that there is
no single winner:

| arm | algo | rung 6 (27 sessions) p95 | highest rung held | cpu core-s @7 |
|---|---|---|---|---|
| `zram-2to1` | lz4 | 39 409 ms | **7** (31/31 ok) | **6.38** |
| `zram-3to1` | lzo-rle | 22 738 ms | 6 | 9.49 |
| `zram-5to1` | zstd | **740 ms** | 6 | 7.25 |

**More compression buys latency until CPU is the scarce resource, then it costs
capacity.** At rung 6 the zram *device* is the constraint (512 MiB holds ~1 GiB of
anonymous memory at 2:1, ~2.5 GiB at 5:1); at rung 7 the *CPU* is, and cheap
compression wins. Choosing 5:1 or 2:1 is choosing which wall you want to hit.

## The harness bug this phase found in itself

`capacity.sh` computed `rate = rung/20` regardless of `HOLD`, so every ratio arm
ran at 1.5× its replica count. The bug is identical across the three ratio arms,
so **the comparison between them stands**; what it invalidates is comparing those
rows with the earlier `none`/`disk`/`zram` rows. Fixed to
`rate = rung / hold_seconds`, and the ladder must be re-run before the two
generations are quoted against each other. The bug and its fix are kept in the
note as §5c rather than removed.

## A note about this phase's dates

The runs are dated 2026-11-02 and 2026-11-03; the notes, the two labs and their
results were recorded on **2026-11-04**. That is why this document's frontmatter
date is the 4th while its subject is the 2nd and 3rd, and why the swap study
carries no `| Date |` row of its own. The gap is real and is stated here instead
of being smoothed into one date.

## Evidence

| Commit | Date | Subject |
|---|---|---|
| `43d2333` | 2026-11-04 | `docs(research): swap/zram capacity findings and the two-layer hypothesis` |
| `f2367fb` | 2026-11-04 | `feat(config): disposable VM lab for the zram/swap capacity experiment` |
| `e845875` | 2026-11-04 | `feat(config): swap pressure lab and sandbox console bundle` |
| `cd9959a` | 2026-11-04 | `feat(vm-swap-lab): Fedora guest, because Ubuntu cannot do zram recompression` |

## What this phase leaves open

Three things, all named in the study rather than implied: the **disk-swap arm**
(§7.1), the **two-layer hypothesis** registered before its run (§5e), and the
decision the platform has not taken — whether any ratio becomes a default, given
that `swapBehavior: LimitedSwap` is still configured as a cushion and not as
capacity (see [ROADMAP §5](../ROADMAP.md)).
