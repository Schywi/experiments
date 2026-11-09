---
title: 'The first run against MinIO: 7 blobs written, 2 sessions booted back'
kind: experiment
state: benchmarked
date: 2026-11-08
branch: persist-probe
tags: [session-lifecycle, persistence, minio, state-benchmarked]
---

# Note 03 — The first run against MinIO: 7 blobs written, 2 sessions booted back

| | |
|---|---|
| Status | Recorded |
| Date | 2026-11-08 |
| Command | `scripts/persist-experiment.sh --sessions 50 --rate 1 --hold 20s --persist-rate 0.2 --restore-rate 0.2` |
| Run id | `20261108T232328Z` — report, `…-persist.json` and `…-restore.json` under `docs/session-lab-runs/` (gitignored) |
| Code | `apps/session-lab` on `persist-probe`, MinIO release `minio` in `sandbox-demo` |
| Related | [01 — the persist gateway](01-persist-gateway-and-budget.md) · [02 — the first persist arm](02-persist-arm-first-run.md) |

Labels: `[M]` printed by this run, `[I]` arithmetic over an `[M]` figure, `[U]` unmeasured.

## 1. What changed since note 02

Two things, and they are the difference between measuring a mechanism and measuring nothing.

- **MinIO is the only store.** The local directory writer is gone, and with it the six flags
  that could point the arm somewhere else. The runner reads the endpoint from Service
  `sandbox-demo/minio` and the credentials from Secret `sandbox-demo/minio-credentials`
  through the kube client it already had. The run's own first line is the proof it took
  that path `[M]`:

  ```
  persist: s3 bucket session-lab prefix "runs/20261108T232328Z/" at s3://session-lab, rate 20%, budget 0 bytes
  ```

- **A restore arm exists.** After the load run, `--restore-rate` boots that fraction of the
  stored blobs on a fresh claim — a new browser, a new context, state read back from MinIO —
  and counts what came back.

Everything else about the workload is unchanged: 50 sessions at 1/s, 20 s each, eight
contexts per Chromium, `https://example.com/`, no actions performed on the page.

## 2. What the run reported `[M]`

| Quantity | Measured |
|---|---|
| sessions planned / stored | 50 / 50, all `success` |
| arrival window | 51.2 s, peak in flight 27, measured mean 14.1 |
| queue wait (claim → ready) | p50 0 ms, p95 40 ms, p99 83 ms, max 161 ms |
| acquire (arrival → ready) | p50 1 ms, p95 40 ms, p99 84 ms |
| hold | p50 20001 ms |
| reset | p50 18 ms, p95 40 ms |
| per-session task time | p50 21 ms, p95 41 ms; script p50 1 ms; heap p50 0.6 MiB |
| instances | 4 warm claims: 18, 17, 11 and 4 sessions |
| node CPU | peak 1.55 / 6 cores, mean 1.55 (n=37) |
| node memory | min available 7077 MiB of 15924 MiB (44.4 %), swap used 4278 MiB |
| resources per session | **0 sessions with samples** — `persist-lab` still supplies no `Sampler` |

## 3. The storage arm `[M]`

```
storage: persist-rate 20%, 7 blobs, 476 bytes retained, 476 bytes ingested,
         0 evicted, mean 68 B, refused 0, failed 0, budget 0
```

- **7 blobs, not 10.** Ids 13, 19, 32, 35, 43, 44, 49 are 14 % of 50 sessions at a 20 %
  rate. That is sampling noise around the hashed selector, not the systematic bias note 02
  §5 recorded: with the hash the fraction is unbiased, and a 50-session binomial has a
  standard deviation near 2.8 sessions `[I]`. A run of a few thousand sessions is what
  settles the rate; a 50-session run cannot.
- **`ingest_bytes` equals `bytes`** because the budget was unbounded and nothing was
  evicted. The two diverge the moment a `--persist-budget` forces the oldest blobs out.
- **68 bytes is the empty blob again** — `{"version":1,"origin":"https://example.com"}`,
  0 cookies and 0 items on every session. This run measures the transport, not the payload.

Capture cost per session `[M]`: 237 ms on the first capture, then 16, 28, 50, 15, 56 and 48 ms.
The first one pays for the S3 client and the bucket creation; the rest sit in note 01 §3's
`5–50 ms [U]` band with one outlier.

## 4. The restore arm `[M]`

```
restore: 2 of 2 selected, ok 2, failed 0, egress 136 bytes, mean 461 ms
```

`--restore-rate 0.2` takes every fifth stored blob `[I]`, so 2 of the 7. Both were fetched
from MinIO, written into a fresh context on a fresh claim, and read back: `cookies_stored 0`,
`items_stored 0`, and the same zeros restored. Two facts follow, and they are worth keeping
apart:

- **The write→read path works end to end.** Objects reached MinIO, `Get` returned them, the
  CDP walk applied them, and a second browser proved it could be done. Nothing before this
  run had ever read a blob back.
- **It does not yet prove that state survives**, because there was no state to survive. The
  zeros are the honest reading of `cookies_restored: 0`.

The cost is real: **461 ms mean per restore**, which is the number a "resume my session"
promise is priced at on this node, cold, with an empty blob.

## 5. The defect this run found

The script's artefact block labels the wrong file:

```
persist: report:  …/20261108T232328Z-restore.json
persist: storage: …/20261108T232328Z-persist.json
```

`ls -t "${out}"/*.json | head -1` picks the newest JSON in the directory, and the restore
file is written after the report, so the run report is never named. Cosmetic, and it makes a
reader open the wrong artefact. The fix is to name the three paths explicitly instead of
globbing.

## 6. What the next run needs

`--seed-bytes` now writes state into each page before the hold, so a run no longer
depends on a website happening to store something. Measured on `https://example.com/`
with `--seed-bytes 65536 --seed-cookies 2`:

| Quantity | Measured |
|---|---|
| seeded | 65536 bytes over 4 keys, plus 2 cookies |
| blob | 49,438 bytes for 4 items and 2 cookies — gzip takes about a quarter off the alphanumeric payload |
| capture | 47 ms and 65 ms |
| restore | 2 of 2 ok, `items_restored 8` of `items_stored 8`, `cookies_restored 4` of 4, mean 392 ms, egress 98,874 bytes |

Two defects had to be fixed before that number existed, both found by the Akali run:

- the restore session never called `Page.enable`, so `Page.loadEventFired` never
  arrived and every blob with `localStorage` died on `wait for load: no
  loadEventFired` after the 30 s timeout;
- seeding ran before the page committed its own document, so `localStorage`
  was the initial `about:blank` origin and the write threw — silently, because
  `Runtime.evaluate` reports a throw as `exceptionDetails`, not as an error. The
  seeder now checks it, and waits for the load first.

The blob also carries the page `url` now, so a restore returns to the page the
session was on instead of the bare origin.

Still `[U]`: IndexedDB (seeded is easy, captured is not implemented), capture CPU
(`persist-lab` passes no `Sampler`), and eviction (`--persist-budget` has never
fired outside unit tests).

## 7. How to check this note

```bash
cat docs/session-lab-runs/20261108T232328Z-persist.json
cat docs/session-lab-runs/20261108T232328Z-restore.json

# the objects are in MinIO, not on a disk the runner wrote to
kubectl -n sandbox-demo exec deploy/minio -- \
  sh -c 'find /data/session-lab/runs/20261108T232328Z -type f | head'

# the runner discovered the store instead of being told about it
sed -n '1,60p' apps/session-lab/cmd/persist-lab/blobstore.go
```
