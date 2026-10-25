# Link contract

Every link is built server-side in `apps/assistant/provenance.py` from a fact.
None is built from model text. A link kind is emitted only when its env is set;
an unset env means that link is omitted, never guessed.

| kind | env | URL | emitted when |
|---|---|---|---|
| `grafana` | `GRAFANA_URL` | `<GRAFANA_URL>/explore?schemaVersion=1&panes=<panes json>` | fact tool is `query_prometheus` with a query |
| `hubble` ("Open in Cilium") | `HUBBLE_URL` | `<HUBBLE_URL>/?namespace=<ns>` | the fact carries a namespace (args, or derived from its resources) |
| `argocd` | `ARGOCD_URL` | `<ARGOCD_URL>/applications/<namespace>/<name>` when the fact names an Application; else `<ARGOCD_URL>/applications` | any fact whose resources mention an Application name |
| `kargo` | `KARGO_URL` | `<KARGO_URL>/projects/<project>/stages/<stage>` when the fact carries a project and stage; else `<KARGO_URL>/projects` | a fact that names Kargo resources |
| `neo4j` | `NEO4J_UI_URL` | `NEO4J_UI_URL` verbatim | fact tool is `get_topology_snapshot` |

Hosts (live, passed by env, not hardcoded):

| UI | address |
|---|---|
| Argo CD (v3.5.2) | `http://192.168.0.247` |
| Kargo (kargo-gateway) | `http://192.168.0.249` |
| Hubble / Cilium UI | `http://192.168.0.241` |
| Grafana | `http://192.168.0.245` |
| Neo4j Browser | `http://192.168.0.242/browser/` |

Route shapes: Argo CD v3 serves Applications at
`/applications/<namespace>/<name>`; Kargo serves Stages at
`/projects/<project>/stages/<stage>`. Both are confirmed against the running
apps before the generators are written; if a shape differs, the generator uses
the overview route (`/applications`, `/projects`) instead of a broken link.

`reproduce` stays separate from `links`: `reproduce` is the copyable command or
PromQL, `links` is the clickable destination.
