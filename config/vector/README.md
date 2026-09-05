# Vector ETL chart

This Helm chart deploys the Vector data-plane receiver for the bounded
Lua/Wasm worm experiment. It does not install Docker, alter containerd, or
execute `kubectl`.

Workers send a JSON batch to `POST /events`:

```json
{"events":[{"worm_id":"pod-uid","sequence":1,"x":1,"y":1,"occurred_at":1725540000000}]}
```

The `expand_batch` remap transform uses Vector's `unnest` behavior to emit one
event per array element. `unwrap_event` removes the batch wrapper and rejects
missing or incorrectly typed fields. The HTTP sink sends one event per request
to `regression.worm-lab.svc.cluster.local:4000/ingest`.

The chart defaults to one replica, a 1,200-event in-memory buffer, and five
sink attempts. Increasing replicas later requires an explicit decision about
duplicate delivery and Elixir state ownership.
