# Questions that must produce links

Run against the GPU assistant (`assistant-e4-gpu`). For each: inspect
`facts[].links` and confirm the expected key is present and the URL is a real
route on the stated host.

| Question | Expected plan | Link that must appear |
|---|---|---|
| `are there any dropped packets?` | `query_prometheus` (LLM-written PromQL) + `get_events` | `grafana` on the prometheus fact |
| `what is the current network traffic?` | `query_prometheus` (LLM-written PromQL) | `grafana` |
| `is anything unhealthy?` | `get_pods` + `get_events` + `query_prometheus` | `grafana`, and `hubble` where a fact carries a namespace |
| `what is happening in the worm-lab namespace?` | `get_pods`/`get_events` with `namespace: worm-lab` | `hubble` (Cilium) |
| `what is the status of the assistant Argo CD application?` | fact naming `assistant` | `argocd` |
| `what is the status of the pilot staging stage?` | fact naming project/stage | `kargo` |
| `what does cartography know about this cluster?` | `get_topology_snapshot` | `neo4j` only if `NEO4J_UI_URL` is set |

Failure to produce a link kind when its env is set is a bug. Producing a link
when its env is unset is also a bug.
