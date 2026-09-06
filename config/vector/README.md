# Vector ETL chart

This Helm chart deploys the Vector data-plane receiver for the bounded
Lua/Wasm worm experiment. It does not install Docker, alter containerd, or
execute `kubectl`.

Workers write one JSON sample to stdout each sampling interval. On the local
single-node k3d cluster, Vector reads only the matching Pod log files from the
read-only `/var/log/pods` mount. `parse_worker_sample` strips the CRI log
prefix, validates the record, and forwards each sample to
`regression.worm-lab.svc.cluster.local:4000/ingest`.

The chart defaults to one replica, a 1,200-event in-memory buffer, and five
sink attempts. Increasing replicas later requires an explicit decision about
duplicate delivery and Elixir state ownership.
