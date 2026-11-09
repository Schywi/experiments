---
title: 'RFC: the persistence writer is a bounded queue in Go, over MinIO'
kind: rfc
state: implemented
date: 2026-11-09
branch: persist-probe
tags: [session-lifecycle, persistence, minio, rfc, state-implemented]
---

# RFC — the persistence writer is a bounded queue in Go, over MinIO

| | |
|---|---|
| Status | Implemented and measured |
| Date | 2026-11-09 |
| Question | Is MinIO being misused as a queue, and what actually bounds a stress run? |
| Code | `apps/session-lab/internal/persist` — `minio.go`, `persist.go`, `capture.go`, `page_state.go` |

## 1. The short answer

MinIO is object storage and stays object storage. The **queue semantics live in our Go writer**,
not in MinIO. `persist.S3` holds a fixed byte budget and, before writing a blob that would cross
it, deletes the oldest blobs until it fits. That makes the bucket a **bounded FIFO store** — new
bytes in, oldest bytes out — with three counters that describe it exactly: `ingest_bytes`,
`bytes` (retained), `evicted_bytes`.

Nothing in MinIO deletes anything: no lifecycle rule, no TTL. If a bucket policy also deleted,
`bytes` would stop describing the store and the budget assertion would be meaningless. The
writer, and only the writer, evicts.

## 2. What is persisted, and by what mechanism

One browser context's restorable state, per persisted session:

| Surface | Mechanism |
|---|---|
| Cookies (HttpOnly included) | CDP `Storage.getCookies` / `Storage.setCookies` with `browserContextId` |
| `localStorage` per origin | CDP `DOMStorage.enable` + `getDOMStorageItems` / `setDOMStorageItem` |
| IndexedDB | page JavaScript: `indexedDB.databases()` → `getAll()`; restore `createObjectStore` + `put` |
| Cache Storage | page JavaScript: `caches.keys()` → `match()` → base64; restore `cache.put(...)` |

Encoded as one gzipped JSON document at `runs/<run-id>/<session>-g<n>.json.gz`. Measured sizes:
110 B for an empty page, 316 B for four cookies, 49 KB for 64 KiB of localStorage or IndexedDB,
62 KB for 64 KiB of Cache Storage — base64 does not compress. Note 05 has the full table.

## 3. The write path, in order

```
1  capture the context over CDP            (the session is still alive)
2  gzip                                    (JSON, roughly 1.3:1 on this content)
3  lock, evict the oldest while retained + size > budget
4  PUT one object                          s3://session-lab/runs/<run>/<session>-g<n>.json.gz
5  record it, count ingest_bytes           (monotonic, never decreases)
```

Eviction happens **before** the PUT, inside the same mutex that guards the write, because a
budget that trims after the write bounds nothing:

```go
for s.budget > 0 && s.bytes+size > s.budget && len(s.objects) > 0 {
    victim := s.objects[0]                 // oldest first
    if err := s.store.Remove(ctx, victim.Key); err != nil { ... }
    s.objects = s.objects[1:]
    s.bytes -= victim.Bytes
    s.evicted += victim.Bytes
}
```

`internal/persist/minio.go`, `S3.Put`. A blob larger than the whole budget is refused and
counted (`refused`), never written.

## 4. Why a stress run cannot blow up the server

| Property | Consequence |
|---|---|
| A byte budget (`--persist-budget`), enforced per write | retained bytes stay under it for the run's life |
| Eviction is oldest-first and precedes the write | no window in which the bucket exceeds the budget |
| One writer, one mutex | two captures cannot both see room for themselves and overshoot |
| Capture is nil-gated (`session.Options.Persist`) | a run that does not ask for persistence touches no storage at all |
| Failures are counted, not retried into a storm | a MinIO outage costs captures (`failed`), never sessions |
| The store sits on a 10 GiB PVC and the budget is typically 32 MiB | the disk ceiling is never approached |

Measured: two sessions of 49 KB blobs produced `ingest 98,874 B, evicted 0`. With a 32 MiB
budget and 49 KB blobs, eviction starts at the 685th blob; `bytes` then stays pinned at 32 MiB
while `ingest_bytes` keeps climbing.

## 5. What this is not

The honest list, so nobody has to infer it:

- **Not a message broker.** No delivery, ack, visibility timeout, consumer offsets or
  at-least-once semantics. Nothing consumes blobs except the restore arm, which reads by key.
- **Not durable ordering metadata.** The ordered object list lives in the writer's memory
  (`s.objects`). A process restart begins with an empty list and cannot evict what this run did
  not write. A long-lived writer needs the persisted ledger described in note 01 §3
  (`reserve` / `commit`) before eviction can be trusted across restarts.
- **Not multi-writer safe.** Two `persist-lab` processes against one prefix would each keep
  their own list and could exceed the budget together. Single-writer is a stated decision
  (note 01, D5), and it is why the store carries no lock.
- **Not a cache.** No read path serves blobs to sessions during a run. Restores read once, at
  the end.

## 6. Public surface

```go
type Writer interface {
    Capture(ctx context.Context, conn *browser.Conn, session *browser.Session,
            id browser.ContextID, sessionID int64) error
}

type Store interface {
    Writer
    Get(ctx context.Context, key string) (Blob, error)
    Objects() []Object          // oldest first
    Stats() Stats
}
```

`Stats` carries `blobs`, `bytes`, `ingest_bytes`, `evicted_bytes`, `refused`, `failed`,
`mean_bytes`, and a per-dataset breakdown (`datasets`). `cmd/persist-lab` writes it to
`<run-id>-persist.json` at the end of a run, and the restore arm writes
`<run-id>-restore.json` beside it.

## 7. One sentence for the reviewer

MinIO holds objects; the queue behaviour — fixed capacity, oldest-first removal, monotonic
ingest accounting — is about thirty lines of `S3.Put` in this package, and it is the reason a
3,000-session stress run cannot fill a disk.
