---
title: '02 — kine, and whether MySQL would help'
date: 2026-10-31
kind: analysis
state: unknown
tags: [cpu, control-plane, state-unknown]
---

# 02 — kine, and whether MySQL would help

## What kine is here

k3s does not run etcd. It runs **kine**: the etcd v3 API implemented over a SQL log. On this node
that log is SQLite, one file, one writer, one filesystem.

Every object in the cluster is a row: pods, secrets, leases, Cilium endpoints, events, and our
`SandboxClaim`s. A `POST /sandboxclaims` is a SQL commit; so is every status update the sandbox
controller writes, every Cilium endpoint it creates, and every Event the cluster emits about them.

## The evidence, from this cluster

`journalctl -u k3s` during a 600-session run:

```
Slow SQL: INSERT INTO kine(…) '/registry/events/sandbox-demo/browser-pool-c2r86.…'   duration=1.54s
Slow SQL: INSERT INTO kine(…) '/registry/cilium.io/ciliumendpoints/…browser-pool-c2r86' duration=2.60s
Slow SQL: INSERT INTO kine(…) '/registry/leases/agent-sandbox-system/…'              duration=2.94s
```

Three unrelated object kinds, 1.5–2.9 s per commit, at the same moment. The consequences look like
platform failures and are one bottleneck:

| Symptom | Mechanism |
|---|---|
| `POST /sandboxclaims: unexpected EOF` | the apiserver died mid-request |
| sandbox controller `leader election lost`, 119 restarts | its Lease write missed the timeout |
| cilium-operator 106 restarts | endpoint writes queue behind the same writer |
| acquisition p95 in minutes | every claim waits behind the commit queue |

Writes per session, minimum: **claim create, sandbox status, event, claim delete, cilium endpoint
create/delete** — five, and a cold start adds pod writes. At 2 sessions/s that is 10 commits/s
through a single SQLite writer with fsync.

## Would MySQL be better?

kine supports MySQL/Postgres as a backend, so this is a real question, not a hypothetical.

| | SQLite (now) | MySQL |
|---|---|---|
| Writers | one file, serialised | row-level locks, concurrent commits |
| Commit latency | fsync per commit on one device | group commit, often faster under concurrency |
| Where it lives | same node, same disk as everything else | another process; same host unless moved off-box |
| Failure modes | `database is locked`, growing log | connection pools, deadlocks, index bloat |
| Operational cost | zero | a database to run, back up, upgrade |

**Expected gain, reasoned from the failure:** the slow statements are *single-row inserts* taking
1.5–2.9 s. That is not lock contention between writers — it is commit latency on one filesystem,
aggravated by 4.1 GiB of swap in use and a node at 88 % memory. MySQL on the same disk inherits the
same fsync; MySQL on a different box removes the disk sharing but adds a network round trip per
commit.

So the honest expectation: **MySQL helps if the bottleneck is lock/queue contention, and helps little
if it is the disk.** Today's evidence points at the disk (slow *individual* inserts, on a swapping
host), which makes datastore choice a second-order fix compared with:

1. **fewer writes per session** (`experiments-9kl`): stop per-pod Event churn, avoid status rewrites,
   reuse claims instead of delete/create. This is the only change that helps on *any* datastore.
2. **measure it** (`experiments-0fo`): `kine_sql_time_seconds` p99 and `kine_sql_total` are already
   exported by the apiserver. Without them, "kine is slow" stays an anecdote, and a datastore
   migration could be paid for and prove nothing.

## What would make MySQL worth doing

Decide with data, in this order:

| Test | Threshold that justifies the migration |
|---|---|
| `kine_sql_time_seconds` p99 while the node is *not* swapping | > 500 ms ⇒ the datastore, not the host |
| Slow SQL under a write-only load (`kubectl create/delete` loop, no browsers) | > 500 ms ⇒ datastore |
| Same load with the browsers running | if it only degrades with browsers, the cause is memory/CPU contention on the host, not kine's engine |
| Writes per session after `experiments-9kl` | if reducing writes does not move p99, the engine is the problem |

## Alternatives to a datastore migration

| Option | Effect | Cost |
|---|---|---|
| Reduce writes per session | fewer commits, on any backend | runner + controller changes |
| Move k3s to external etcd | removes the SQL log entirely | another datastore, more memory on a 15 GiB node |
| Put SQLite on a faster/dedicated disk | removes fsync contention with swap | host work; the node already swaps 4 GiB |
| Cap sessions/s (`experiments-c4h`) | stays inside what kine survives today | nothing but a decision |
| Accept it and measure | makes any future change provable | `experiments-0fo` |

**Recommendation:** do `experiments-0fo` (metrics) and `experiments-9kl` (fewer writes) before
considering MySQL. If both land and `kine_sql_time_seconds` p99 stays above ~500 ms on a node that
is not swapping, MySQL or external etcd becomes a defensible next step — and by then the numbers to
justify it will exist.

---

## Where this sits

- Collection: [cpu-oversubscription](README.md)
