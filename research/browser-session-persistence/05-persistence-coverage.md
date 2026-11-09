---
title: 'Persistence coverage: what we save, how big it is, and how it is read back'
kind: analysis
state: benchmarked
date: 2026-11-09
branch: persist-probe
tags: [session-lifecycle, persistence, minio, coverage, state-benchmarked]
---

# 05 — Persistence coverage: what we save, how big it is, how it is read back

| | |
|---|---|
| Status | Measured on this node |
| Date | 2026-11-09 |
| Runs cited | `20261109T153249Z` (idb + cache), `20261109T153319Z` (six-label mix), `20261109T002357Z` (cookies + localStorage on a real site) |
| Code | `apps/session-lab/internal/persist`, `cmd/persist-lab`, `config/minio` |
| Related | [01 — the persist gateway](01-persist-gateway-and-budget.md) · [03 — the first MinIO run](03-first-minio-run.md) · [04 — the stress runbook](04-stress-runbook.md) |

## 1. What a blob holds

One browser context's restorable state, as a gzipped JSON document. Nothing above the store
parses it.

```json
{
  "version": 1,
  "origin": "https://example.com",
  "url": "https://example.com/",
  "cookies": [ { "name": "seed-0", "value": "...", "domain": "example.com", ... } ],
  "local_storage": { "https://example.com": [ { "name": "seed-0", "value": "..." } ] },
  "indexeddb": [ { "name": "seed-db", "version": 1,
                   "stores": [ { "name": "items", "key_path": "id", "records": ["{\"id\":0,...}"] } ] } ],
  "cache_storage": [ { "name": "seed-cache",
                       "entries": [ { "url": "https://example.com/seed-0",
                                      "content_type": "text/plain",
                                      "body_base64": "..." } ] } ],
  "cache_probe": "caches=object names=[\"seed-cache\"]",
  "cache_error": "", "indexeddb_error": ""
}
```

`url` is the page, not just the origin, so a restore returns to where the work was.

## 2. Coverage, surface by surface

| Surface | Saved | Read and written by | Restored |
|---|---|---|---|
| Cookies (HttpOnly included) | yes | CDP `Storage.getCookies` / `Storage.setCookies` with `browserContextId` | before the first navigation |
| `localStorage` per origin | yes | CDP `DOMStorage.enable` + `getDOMStorageItems` / `setDOMStorageItem` | after navigating to the page |
| IndexedDB | yes | page JavaScript: `indexedDB.databases()` → `open` → `getAll()`; restore `createObjectStore` + `put` | after navigating, then `Page.reload` |
| Cache Storage | yes | page JavaScript: `caches.keys()` → `cache.keys()` → `match()` → base64; restore `cache.put(new Request, new Response)` | after navigating, then `Page.reload` |
| `sessionStorage` | **no** | — | cannot survive the navigation a restore needs |
| HTTP cache | **no** | — | would need a disk-cache copy; the 5 MiB row of note 01 §6 |
| Service workers, Cache API programs | **no** | — | not captured |
| DOM, tab history, JS heap, sockets | **no** | — | a restore reloads the page |

IndexedDB and Cache Storage are not reachable through CDP alone: IndexedDB's domain only
reads, and Cache Storage has no domain at all. Both are read and written with
`Runtime.evaluate` on the session's own page — one mechanism instead of two, available exactly
while the page that holds the state is alive. The cost is that a page whose origin has no
document carries neither surface.

`cache_probe`, `cache_error` and `indexeddb_error` exist because a surface that fails to read
used to come back empty with no trace: the first cache run reported 77-byte blobs while the
seeder had written eight entries, and the blob said nothing about why.

## 3. Size per tier, measured `[M]`

Seeded on `https://example.com/`, one session per row, `--persist-rate 1`.

| Dataset | Written into the page | Blob (gzipped) | Restored |
|---|---|---|---|
| `none` | nothing | **110 B** | — (the page's own state: 0 cookies, 0 items) |
| `cookies-4` | 4 cookies, 16 chars each | **316 B** | 4 of 4 |
| `local-64k` | 65,536 B over 4 keys | **49,438 B** | 4 of 4 items |
| `idb-64k` | 65,536 B over 8 records | **49,393 B** | 8 of 8 records |
| `cache-64k` | 65,536 B over 8 entries | **61,865 B** | 8 of 8 entries |
| `session-8k` | 8,192 B over 4 keys | **110 B** | 0 of 0 — not captured, by definition |
| real page, `universe.leagueoflegends.com` | the site's own state | **1,138 B** | 4 cookies + 1 item captured; restore not completed that run |

Two ratios worth carrying: alphanumeric localStorage compresses about 1.3:1 (65,536 → 49,438),
and Cache Storage does not compress at all (65,536 → 61,865) because the bodies are base64
inside the JSON.

Against note 01 §6's projection rows: 25 KiB is a cookie-only session, 250 KiB is
`local-256k`, 1 MiB is `local-1m` or `cache-1m`, and the 5 MiB row is a real HTTP-cache
capture that this build does not do.

## 4. Where it lives and how it is read

| | |
|---|---|
| Store | MinIO `192.168.0.251:9000`, bucket `session-lab` |
| Key | `runs/<run-id>/<session-id>-g<generation>.json.gz` |
| Writer | `persist.S3` over `minio-go`, one PUT per blob, oldest-first eviction to stay under `--persist-budget` |
| Restorer | `cmd/persist-lab --restore-rate`, which `GET`s each blob and boots a fresh context from it |

Reading a blob back, three ways, all used here:

```bash
# 1. the console API, no credentials (the gateway logs in for you)
curl -s "http://192.168.0.252/api/v1/buckets/session-lab/objects/download?prefix=runs/<run-id>/000001-g1.json.gz" \
  | gunzip | head -c 400

# 2. what the store holds for one run — MinIO keeps a directory per object with an xl.meta
kubectl -n sandbox-demo exec deploy/minio -- \
  sh -c 'find /data/session-lab/runs/<run-id> -name xl.meta | wc -l'

# 3. the run's own counters
cat docs/session-lab-runs/<run-id>-persist.json    # blobs, ingest_bytes, evicted_bytes, datasets
cat docs/session-lab-runs/<run-id>-restore.json    # ok, incomplete, per-surface restored/stored
```

The console at `http://192.168.0.252/` shows the same objects under Buckets → `session-lab` →
`runs/`.

## 5. What a run reports per dataset

`<run-id>-persist.json` carries a `datasets` block — sessions and bytes per label — so a run
over a mix is readable per class instead of as one aggregate:

```json
"datasets": [ { "label": "cache-32k", "sessions": 2, "bytes": 62160 },
              { "label": "cookies-4", "sessions": 1, "bytes": 316 },
              { "label": "session-8k", "sessions": 2, "bytes": 220 } ]
```

A `session-8k` row with a non-trivial byte count is itself a finding: that surface is captured
by nothing, so the blob only holds the page's own cookies and items.

## 6. Limits this coverage leaves open

- **IndexedDB volume.** The page-side read loads every record into one JSON string. A database
  of tens of MiB will hit the evaluate message limit long before the store does. Pagination is
  the fix, and the CDP domain has `skipCount`/`pageSize` for it.
- **Cache Storage bodies.** base64 inflates by 4/3 and does not compress; a 1 MiB cache is a
  1.4 MiB blob.
- **Service workers and the HTTP cache** are outside the blob, so a restored session
  re-downloads what the page needs.
