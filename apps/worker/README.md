# Lua/Wasm worm worker

This directory contains the application logic for exactly one worm per
Kubernetes Pod.  It has no Kubernetes client, ServiceAccount access, or
replica-count knowledge.  A separate Go controller decides whether a
replication intent becomes another Pod.

## Contract

The WASI component host injects a `worm_host` capability with environment,
time, sleep, and outbound-HTTP operations.  It must expose only the two
configured destinations; it must not expose the Kubernetes API.

Required environment variables:

| Variable | Meaning |
| --- | --- |
| `WORM_ID` | Immutable identity for this Pod's worm, normally the Pod UID. |
| `VECTOR_EVENTS_URL` | Full internal Vector endpoint, for example `http://vector.worm-lab.svc.cluster.local:8080/events`. |
| `REPLICATOR_URL` | Internal controller base URL, for example `http://replicator.worm-lab.svc.cluster.local:8081`. |

At startup, the worker sends one bounded replication intent to
`POST ${REPLICATOR_URL}/v1/replication-intents`:

```json
{"worm_id":"…","intent_id":"…:1"}
```

Every ten sampling intervals it posts this fixed-size Vector batch to
`VECTOR_EVENTS_URL`:

```json
{"events":[{"worm_id":"…","sequence":1,"x":1725540000000,"y":1,"occurred_at":1725540000000}]}
```

The queue/batch capacity is 10 samples and every outbound payload is attempted
at most five times.  A failed batch is dropped, which bounds memory and avoids
a retry storm.  `intent_id` is stable for the worker lifetime so controller
deduplication can make an ambiguous delivery safe.

## WASI component boundary

`worker.lua` is intentionally portable Lua, not a Linux-container script.
It expects a narrow host import named `worm_host`; the future pinned
Lua-to-WASI component adapter owns the concrete Wasmtime/WASI Preview 2
`wasi:http/outgoing-handler` implementation.  This separation is necessary:
raw Lua has no safe built-in HTTP client, and granting the guest generic
network or Kubernetes credentials would break the experiment's topology.

The OCI package is base-image-free.  Once a reproducible component build has
written `dist/worm.component.wasm`, package it with:

```sh
make package IMAGE=registry.example/worm-worker:0.1.0
```

The deployed Pod must use `runtimeClassName: wasmtime` and pass the three
variables above.  Its resource limit—not this process—is the hard per-worm
memory guard.

## Offline verification

Run `make test`.  It uses only POSIX shell and statically verifies the public
protocol, retry/batch bounds, OCI scratch artifact, and absence of Kubernetes
access.  It intentionally does not require Lua, Wasmtime, Docker, k3d, or
`kubectl`.
