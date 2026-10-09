# Feature (proposed): `get_topology` / `get_topology_snapshot`

> Status: **implemented** (branch `native-k3s-gpu-models`). Both `get_topology`
> (live, k8s API) and `get_topology_snapshot` (Cartography/Neo4j) are now
> registered level-1 tools. The design below is retained as the rationale.

## The question this answers

*"Give the agent a high-level map of the cluster."* — but "topology" is three
different things, from three sources with different freshness. Collapsing them
into one tool hides staleness, which is the one failure an SRE cannot tolerate.

## Three concerns, three sources

| Concern | Source | Freshness | Covers | Does NOT cover |
|---|---|---|---|---|
| **Structure** | Kubernetes API | live | ownership (`Deployment→ReplicaSet→Pod`), selectors/`EndpointSlice` edges, `Service`/`Ingress` routing, ConfigMap/Secret refs, node placement, health/status | observed traffic |
| **Network** | Cilium / Hubble | live | observed flows (relay), L7 (HTTP/gRPC/DNS), drops, the service map | ownership/config/health |
| **Snapshot** | Cartography / Neo4j | **≤6h** (`CronJob` `0 */6 * * *`) | the ingested asset/relationship graph | live state |

**Key point:** the Kubernetes-API structure map is **not** redundant with Cilium.
Cilium observes *network* — it cannot tell you who *owns* a Pod, which Service
*selects* it, or which ConfigMap it mounts. Equally, the k8s API can't observe
traffic. Let each own its layer.

## Proposed tools

### `get_topology(namespace?, depth?)` — live structure

- **Source:** the Kubernetes API, as the assistant's read-only ServiceAccount
  (already covers `get/list/watch`).
- **Returns** a **bounded graph**, not YAML:

  ```json
  {"scope": "worm-lab",
   "nodes": [{"kind": "Deployment", "ns": "worm-lab", "name": "worm-worker", "ready": "1/1"}],
   "edges": [{"t": "owns",    "from": "Deployment/worm-worker", "to": "ReplicaSet/worm-worker-…"},
             {"t": "selects", "from": "Service/worm-worker",    "to": "Pod/worm-worker-…"}],
   "rollup": {"pods": 12, "unhealthy": 0}}
  ```
- **Edges:** `owns` (ownerReferences), `selects` (Service/EndpointSlice), `routes`
  (Ingress→Service), `mounts` (ConfigMap/Secret), plus `node` placement.
- **Explicitly NOT network flows** — that is Cilium's layer (below).
- **Size:** one fact, so ≤ `PER_FACT_CHARS` (600) typically — collapse big scopes
  to `(kind,count)`, cap nodes/edges, honour `scope`.
- **Provenance:** `source: kubernetes`, `reproduce.cli` = a `kubectl` equivalent
  (`kubectl get deploy,rs,pod,svc,ing -n NS -o …`); no deep link in v1.

### `get_topology_snapshot(query?)` — Cartography graph

- **Source:** the deployed **Cartography → Neo4j** (`cartography-neo4j:7687`,
  bolt). Ingested by a `CronJob` every 6 hours.
- **Returns** the graph's answer to a bounded question (workloads, relationships,
  cross-references the live API doesn't hold).
- **Must stamp freshness.** Its provenance `at` is the **CronJob's last
  successful ingest**, not the query time, and the answer says "as of <age>".
  This is the single non-negotiable detail that keeps two topology tools from
  being confusing.
- **Provenance:** `source: cartography`, `reproduce.cypher` = the exact query,
  `links.neo4j` = the Browser (`http://neo4j.home.arpa/` — already ingressed).
- **New dependencies:** bolt access + network policy + (later) RBAC for Neo4j.

### Network — do **not** rebuild; extend Cilium's

- **Today:** `query_prometheus` with the curated `hubble_*` catalog
  (drops, DNS, HTTP, flows) — aggregate, already shipped.
- **Later (optional):** `get_network_topology()` fed by the **Hubble relay**
  (per-flow, gRPC) for the service map. New dependency; not v1.

## Why not one tool

A single `get_topology` that mixed live API, Hubble flows, and a 6h-old graph
would report **three different freshnesses under one name** — the exact ambiguity
to avoid. Separate tools keep each answer's age honest.

## Selection cost

Three tools raise the tool-selection surface at 1.5B. Mitigate with distinct
names/descriptions and deterministic `_BUNDLES` routing
("topology"/"what talks to what" → the right one).

## Open decisions (need the user)

1. **Build it at all?** Currently frozen.
2. **Snapshot source:** Cartography/Neo4j (rich, ≤6h stale, new bolt dependency)
   vs k8s-API-only (live). Recommendation: if built, build the **live structure**
   tool first; add the snapshot only if the ingested graph answers something the
   live API cannot.
3. **RBAC for bolt** and a staleness threshold for the snapshot.
4. **One vs two vs three tools.**

## Recommendation (unchanged from `research/reasoner-and-topology.md`)

Build **one** bounded `get_topology` from the **Kubernetes API** first (live
structure). Keep Cilium/Hubble as the network layer (already partly wired). Treat
the Cartography snapshot as a **separate, later** tool — and only if it earns its
keep against the live API.
