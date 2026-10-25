# Link contract

A link is built server-side in `apps/assistant/provenance.py` from a fact, never
from model text. **One kind per answer**, chosen by the intent, and only when the
question needs it. An unset env drops that kind; nothing is guessed.

| kind | intent | env | URL | fires only when |
|---|---|---|---|---|
| `grafana` | `prometheus` | `GRAFANA_URL` | `<GRAFANA_URL>/explore?schemaVersion=1&panes=<panes json>` | the fact is `query_prometheus` **and** `ok == true` |
| `hubble` ("Open in Cilium") | `kube` | `HUBBLE_URL` | `<HUBBLE_URL>/?namespace=<ns>` | the fact carries `args.namespace` |
| `neo4j` | `cartography` | `NEO4J_UI_URL` | `NEO4J_UI_URL` verbatim | the fact is `get_topology_snapshot` |

`general` emits nothing. There is no `argocd` or `kargo` kind.

Intent is derived in `pipeline._intent()` from the facts actually used:
`cartography` > `prometheus` > `kube` > `general`.

Hosts (live, passed by env, never written in code):

| UI | address |
|---|---|
| Grafana | `http://192.168.0.245` |
| Hubble / Cilium UI | `http://192.168.0.241` |
| Neo4j Browser | `http://192.168.0.242/browser/` |

Why no Argo CD or Kargo link: argocd-server returns 404 for every SPA path and
sends `X-Frame-Options: sameorigin`, so no URL of it can be opened or framed;
Kargo is path-routed but cross-origin and unscreenable. Both stay out until the
client owns that navigation.

`reproduce` stays separate from `links`: `reproduce` is the copyable command,
PromQL or Cypher.
