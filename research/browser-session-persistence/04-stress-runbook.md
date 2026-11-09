---
title: 'Persistence stress runbook — the commands'
kind: handoff
date: 2026-11-09
branch: persist-probe
tags: [session-lifecycle, persistence, minio, runbook]
---

# Persistence stress runbook

Every command here runs from the repository root unless it says otherwise.

```bash
cd /home/dev/Documentos/git_repos/experiments-persist
```

Live addresses this runbook assumes:

| | |
|---|---|
| MinIO S3 API | `192.168.0.251:9000` |
| MinIO console, no login | `http://192.168.0.252/` |
| Namespace / pool | `sandbox-demo` / `browser-pool` |
| Blob prefix in the bucket | `session-lab/runs/<run-id>/` |

---

## 0. Preconditions

```bash
kubectl get --raw=/readyz >/dev/null && echo "apiserver ok"
kubectl get node --no-headers | grep ' Ready '
kubectl -n sandbox-demo get pods | grep -E 'minio|browser-pool' | head
kubectl -n sandbox-demo get svc minio minio-console -o wide
```

`minio` must be `1/1 Running`, `minio-console` must have `EXTERNAL-IP 192.168.0.252`.

Check the store answers, with no credentials:

```bash
curl -s -o /dev/null -w 's3: %{http_code}\n'      http://192.168.0.251:9000/minio/health/ready
curl -s -o /dev/null -w 'console: %{http_code}\n' http://192.168.0.252/
curl -s http://192.168.0.252/api/v1/buckets
```

---

## 1. Probe before a long run (about 1 minute)

Two sessions, fully persisted and restored, with seeded state. This is the run that
tells you whether capture and restore work at all before spending ten minutes on arms.

```bash
scripts/persist-experiment.sh \
  --sessions 2 --rate 1 --hold 25s \
  --persist-rate 1 --restore-rate 1 \
  --seed-bytes 65536 --seed-cookies 2 \
  --target-url https://example.com/
```

Read four lines of the output:

```
persist: seeded session 1 with 65536 bytes over 4 keys and 2 cookies (localStorage now holds 4 keys)
persist: session 1 49438 bytes (2 cookies, 4 items) in 47ms
storage: persist-rate 100%, 2 blobs, 98874 bytes retained, 98874 bytes ingested, 0 evicted, mean 49437 B
restore: 2 of 2 selected, ok 2, failed 0, egress 98874 bytes, mean 392 ms
```

`ok` equal to `selected`, `items_restored` equal to `items_stored`, `cookies_restored`
equal to `cookies_stored`. If `ok` is 0 with `wait for load: no loadEventFired`, the
restore arm is broken again.

The same probe against a real site, which is what shows whether a page's own state is
captured:

```bash
scripts/persist-experiment.sh \
  --sessions 2 --rate 1 --hold 25s \
  --persist-rate 1 --restore-rate 1 \
  --target-url 'https://universe.leagueoflegends.com/pt_BR/champion/akali/'
```

---

## 2. The stress run

```bash
scripts/persist-stress.sh \
  --sessions 300 --rate 3 --hold 30s \
  --persist-rate 0.2 --restore-rate 0.3 \
  --persist-budget 33554432 \
  --seed-bytes 65536 --seed-cookies 2
```

What it does, in order: control arm (`--persist-rate 0`, nothing persists), persistence
arm (20 % persists, 30 % of those restored afterwards, 32 MiB budget, 64 KiB seed), then it
counts the objects MinIO holds for each run and fails when that disagrees with the
writer's `blobs`.

## 2a. The dataset matrix

`--seed-mix` is the corpus: each label names a surface and a size, and the weights decide how
many sessions carry it. Assignment is deterministic by session id.

```bash
scripts/persist-lab ... --seed-mix 'local-64k:10,local-256k:5,local-1m:1,idb-256k:3,cache-1m:1,cookies-4:5,none:5'
```

| Label | Writes | Captured |
|---|---|---|
| `cookies-N` | N cookies on the origin | yes |
| `local-64k`, `local-256k`, `local-1m` | localStorage of that size | yes |
| `idb-256k` | IndexedDB, one store, records | yes — read and written through the page |
| `cache-1m` | Cache Storage entries | yes — read and written through the page |
| `session-8k` | sessionStorage | **no**, by definition: it does not survive a navigation |
| `none` | nothing | — |

A 20-session run over a five-label mix:

```bash
scripts/persist-experiment.sh --sessions 20 --rate 1 --hold 20s \
  --persist-rate 1 --restore-rate 1 \
  --seed-mix 'local-64k:1,local-256k:1,idb-128k:1,cache-128k:1,cookies-4:1' \
  --target-url https://example.com/
```

`<run-id>-persist.json` gains a `datasets` block — sessions and bytes per label — so the run
is readable per class instead of one aggregate.

Measured on `example.com` (run `20261109T153249Z`, two sessions, cache and idb at 64 KiB):

| Surface | Blob | Restored |
|---|---|---|
| `idb-64k` | 49,393 B | 8 of 8 records |
| `cache-64k` | 61,865 B | 8 of 8 entries |

Cache bodies are base64, which is why 64 KiB of seeded text lands at ~62 KiB in the blob
while the same size of IndexedDB lands at ~49 KiB.

Ends with one line per arm:

```
arm=control  blobs=0      ingest=0        evicted=0     refused=0  restore_ok=0    objects_on_store=0
arm=persist  blobs=60     ingest=2963280  evicted=0     refused=0  restore_ok=20   objects_on_store=60
```

Shapes worth running, one at a time:

```bash
# no budget: ingest only, nothing is ever evicted
scripts/persist-stress.sh --persist-budget 0

# small budget: eviction fires, oldest blobs leave to make room
scripts/persist-stress.sh --sessions 600 --persist-rate 1 --persist-budget 16777216

# heavy blobs: 1 MiB of localStorage per session
scripts/persist-stress.sh --seed-bytes 1048576 --persist-budget 268435456

# a different pool, and no control arm
PERSIST_POOL=browser-pool-gpu scripts/persist-stress.sh --no-control

# a real site instead of seeded state (no --seed-bytes: its own state is the payload)
PERSIST_TARGET_URL='https://universe.leagueoflegends.com/pt_BR/champion/akali/' \
  scripts/persist-stress.sh --sessions 20 --rate 1 --hold 25s --persist-rate 1 --restore-rate 1
```

---

## 3. Confirm the store, independently of the run's own report

```bash
run=<run-id from the output>            # e.g. 20261109T010203Z-persist

# the store's own filesystem: one directory per object
kubectl -n sandbox-demo exec deploy/minio -- \
  sh -c "find /data/session-lab/runs/${run} -name xl.meta | wc -l"

# the run's own counters
cat docs/session-lab-runs/${run}-persist.json
cat docs/session-lab-runs/${run}-restore.json

# one object's contents, through the console API (no credentials needed)
curl -s "http://192.168.0.252/api/v1/buckets/session-lab/objects/download?prefix=runs/${run}/000001-g1.json.gz" \
  | gunzip | head -c 400
```

The blob is `{"version":1,"origin":"…","url":"…","cookies":[…],"local_storage":{…}}`.
`url` is the page the session was on; a restore navigates to it.

---

## 4. Watch it in the UI

Open **http://192.168.0.252/** — no login, the gateway authenticates for you. Then
**Buckets → `session-lab` → `runs/`**; each run is a prefix named after its run ID and
holds one `<session>-g<n>.json.gz` per persisted session.

If the page spins forever, the gateway is buffering a long-lived console request; the
direct console at `http://192.168.0.251:9001` takes the root credentials from
`kubectl -n sandbox-demo get secret minio-credentials -o jsonpath='{.data.accessKey}' | base64 -d`.

---

## 5. What to write down

| Quantity | Where it comes from |
|---|---|
| sessions planned / stored, outcomes | the run's report block in the log |
| `blobs`, `ingest_bytes`, `bytes`, `evicted_bytes`, `refused`, `failed` | `<run-id>-persist.json` |
| `selected`, `ok`, `failed`, `egress_bytes`, items and cookies restored | `<run-id>-restore.json` |
| objects actually held | the `find … -name xl.meta | wc -l` count |
| capture cost per session | the `persist: session N <bytes> bytes … in <ms>ms` lines |
| control versus persistence | the two `arm=` lines |

The numbers this workspace has measured so far, for comparison: capture 47–65 ms on a
49 KiB blob, restore 392–461 ms, 65,536 seeded bytes → 49,438 in the blob.

---

## 6. Afterwards

```bash
# blobs stay; claims are removed by the script. This is the manual sweep:
kubectl -n sandbox-demo delete sandboxclaims -l app.kubernetes.io/managed-by=session-lab --wait=false

# every persisted run, newest first
ls -t docs/session-lab-runs/*-persist.json | head

# total objects in the bucket
curl -s http://192.168.0.252/api/v1/buckets
```

Deleting blobs is the budget's job. The one manual way to start clean:

```bash
kubectl -n sandbox-demo exec deploy/minio -- sh -c 'rm -rf /data/session-lab/runs'
```

---

## 7. Known limits of the harness

- `persist-lab` has no `--mix` and no `--fixed-arrival`: one hold length, Poisson arrivals.
- IndexedDB is not captured, so a page that keeps state only there produces an empty blob.
- Capture CPU and memory are not measured: `persist-lab` passes no `Sampler`, so the
  report's resource rows are zeros.
- The budget invariant is checked at the end of a run, not sampled during it.
