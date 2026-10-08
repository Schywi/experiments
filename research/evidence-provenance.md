# Evidence provenance, deep links, and cartography — investigation + plan

Design + implementation plan. **No code changed by this document.** Grounded
against the live cluster and branch `native-k3s-gpu-models` (2026-10-07).

Scope, kept deliberately small:

> The agent tells me what it found, shows me where the evidence came from, and
> gives me a one-click or copyable way to independently verify it.

Not an observability platform.

---

## Part 0 — Access model: replace `/etc/hosts` with Cilium LB + real DNS

**This is a prerequisite for everything below** — a deep link is only useful if
the host in it actually resolves.

### What's wrong today

- Ingress hosts are `*.localhost`: `argocd.localhost`, `assistant.localhost`,
  `grafana.localhost`, `victoriametrics.localhost`, `neo4j.localhost`,
  `cartography.localhost`, and `localhost` (hubble-ui).
- `*.localhost` is **reserved to loopback** (RFC 6761) — browsers and resolvers
  force it to `127.0.0.1`. The Cilium Ingress is *not* on loopback; it is the
  shared LoadBalancer at **`192.168.0.240`**.
- So `scripts/hosts-entries.sh` writes `/etc/hosts` entries mapping those
  `.localhost` names → `192.168.0.240`, **fighting the resolver**. Fragile
  (browsers may ignore it for `.localhost`), machine-local, and undocumented
  drift.

Current Cilium LB IPAM addresses (from `lan-pool` `192.168.0.240-250`):

| Service | LB IP |
|---|---|
| `cilium-ingress` (shared ingress) | `192.168.0.240` |
| `hubble-ui` | `192.168.0.241` |
| `cartography-neo4j` | `192.168.0.242` |
| `assistant` | `192.168.0.243` |

### The proper model

Route HTTP apps through **one shared Cilium Ingress** reached at the LB IP, and
give each a **DNS-resolvable hostname** — no `/etc/hosts`.

Options, in order of preference:

1. **Wildcard DNS → the LB IP (recommended).** Point a DNS name at the shared
   ingress LB and name services under it:
   - `*.192.168.0.240.nip.io` (public wildcard DNS; any `<name>.192.168.0.240.nip.io`
     resolves to `192.168.0.240`), or a LAN resolver entry
     `address=/apps.home/192.168.0.240` (Pi-hole/Unbound/dnsmasq).
   - Ingress hosts become `grafana.192.168.0.240.nip.io`,
     `argocd.192.168.0.240.nip.io`, `assistant.192.168.0.240.nip.io`, …
   - **Browser-resolvable with no host file**; the RFC is respected.
2. **Path-based on one host** (`http://apps.192.168.0.240.nip.io/grafana`).
   Fewest names, but Grafana / VictoriaMetrics / Argo CD / Neo4j serve
   absolute-path assets and need per-app sub-path config — more friction.
3. **LAN DNS wildcard** if you run an internal resolver — best privacy, but it's
   router config outside this repo.

**Actions (design-level):**
- Change `config/{grafana,victoriametrics,cartography,argocd,assistant}` ingress
  `host:` values from `*.localhost` to the wildcard form; add a **wildcard host**
  on the shared Cilium ingress so new services need no per-host change.
- **Pin** the shared ingress LB IP (so links are stable), or template links from
  the discovered `cilium-ingress` IP.
- **Delete `scripts/hosts-entries.sh`** and the `/etc/hosts` workflow; keep the
  assistant's own LB (`192.168.0.243`) as the "no-hosts" fallback it already is.
- The `UI_LINKS` in `apps/assistant/chart/values.yaml` become wildcard hosts, and
  deep links (Part 3) are generated against **resolvable** hosts.

Until this lands, every "deep link" is only as good as the host file.

---

## Part 1 — Evidence provenance

### What already exists (the starting point)

- `pipeline.run` returns `facts = [{"tool","args","ok","evidence","ms"}]` —
  **tool + args + latency are captured per invocation**, separate from the
  summarize prompt.
- `k8sutil.audit` writes JSONL (`tool_call{tool,args,level}`, `prom_query{query}`).
- `observability.query_prometheus` **returns its `query`**;
  `search_logs` returns `{namespace, contains,...}`.
- `ui.py` renders only a **tool chip** + timings — no args, no queries, no links.

### Proposed per-invocation metadata

```json
{
  "tool": "query_prometheus",
  "args": {"query": "sum(rate(hubble_drop_total[5m])) by (reason))"},
  "ok": true, "ms": 12.4, "at": "2026-10-07T15:04:05Z",
  "source": "victoriametrics",
  "reproduce": {
    "promql": "sum(rate(hubble_drop_total[5m])) by (reason))",
    "cli": "curl -sG 'http://<host>/api/v1/query' --data-urlencode \"query=<promql>\""
  },
  "links": { "grafana": "<server-built Explore URL>" }
}
```

```json
{
  "tool": "get_pods", "args": {"namespace": "worm-lab"}, "ms": 8.1,
  "source": "kubernetes",
  "reproduce": {"cli": "kubectl get pods -n worm-lab"},
  "links": {"hubble": "http://<hubble-host>/?namespace=worm-lab"}
}
```

Every requested field maps to a real source: tool/args (facts), PromQL (tool
result), namespace/resource (args), time/range (`time` param + `at`), VM endpoint
(`VM_URL`), Cilium/Hubble (PromQL + `namespace`), Grafana (built from the query),
Hubble (namespace label).

---

## Part 2 — Reproducibility (per tool)

| Tool | Reproduce with |
|---|---|
| `get_pods` | `kubectl get pods [-n NS]` |
| `get_pod_logs` | `kubectl logs POD -n NS --tail N` |
| `get_events` | `kubectl get events [-n NS]` |
| `search_logs` | `kubectl logs -n NS --all-containers \| grep -i 'X'` |
| `query_prometheus` | exact **PromQL**; `curl` VM `/api/v1/query?query=…[&time=…]` |
| `search_knowledge` / `remember` / `recall` | local files/JSONL — **not** externally reproducible |

All exact, because **the model never authors queries** (curated catalog + regex).

---

## Part 3 — Deep links (verified classification)

| Source | Class | Evidence |
|---|---|---|
| **Grafana Explore** | **Direct deep link possible** | Deployed **Grafana 13.2.3** (`/api/health`). Explore panes URL encodes datasource + PromQL + range: `/explore?schemaVersion=1&panes={"a":{"datasource":"<UID>","queries":[{"refId":"A","expr":"<promql>","datasource":{"type":"prometheus","uid":"<UID>"}}],"range":{"from":"now-1h","to":"now"}}}`. Dashboard vars: `/d/<uid>/?var-x=y`. |
| **VictoriaMetrics** | **Reproducible; link with limitations** | `/api/v1/query` exact; VMUI deep links (`/vmui/?g0.expr=…`) exist, but the VM Service is **ClusterIP, no ingress** — a browser link needs exposure. |
| **Hubble UI** | **Possible with limitations** | Deployed **v0.13.5**; the bundle parses `location.search`/`URLSearchParams` (30+ refs) and references `namespace=` → a **namespace-scoped** link (`?namespace=NS`) is supported. **No evidence of an arbitrary flow-query link** (type/verdict/from/to). |
| **Hubble CLI** | **Reproducible** | `hubble observe --namespace NS [--verdict DROPPED]` — exact, CLI not URL. |
| **k8s tools** | **No deep link; reproducible** | the `kubectl` command |

**Verified, not assumed:** the deployed Hubble UI reads URL params and supports at
least a **namespace** filter → *"possible with limitations"*; exact **flows** have
**no** confirmed deep link → fall back to a copyable `hubble observe`.

---

## Part 4 — LLM context vs human metadata (can it bypass the LLM?)

**Yes — and the pipeline already separates them.** `correlate` sees only
`evidence[:2600]`/`fact[:600]`; the raw `facts` (with args) sit beside it. So
provenance + links go on a **separate response field / endpoint**, never into
`plan`/`correlate`:

- **LLM sees:** `{summary, fact excerpts, source}` (~2600 chars, unchanged).
- **Frontend sees:** full `facts` + `reproduce` + `links`, built **server-side
  after** correlate.

**Token cost to the LLM: 0.**

---

## Part 5 — Tool-call visibility (UI evidence trail)

Fed from the separate field, so no extra LLM tokens:

```
Answer
└─ Evidence (3)
   ├─ get_pods · namespace=worm-lab · 8 ms    → kubectl get pods -n worm-lab   [copy]
   ├─ query_prometheus · 12 ms                → PromQL: sum(rate(hubble_drop_total[5m])) by (reason))   [copy] [Open in Grafana]
   └─ query_prometheus · 9 ms                 → PromQL: sum(rate(hubble_flows_processed_total[5m])) by (verdict))  [copy] [Open in Grafana]
```

Collapsed by default; each row ≤ ~120 chars.

---

## Part 6 — Security

- **Never emit internal service URLs** (`*.svc.cluster.local`) to the browser —
  only resolvable ingress hosts or assistant-origin paths.
- **Generate URLs server-side** from trusted `facts`; the model must not mint
  links.
- **Auth:** this lab is no-auth/local, so any host works *here*; design for a
  hostile deployment by proxying observability URLs through an **authenticated
  assistant-origin path** rather than linking internal endpoints directly.
- **Don't reflect free text** (corpus/memory content) into URLs.
- Namespaces/resource names: low sensitivity locally; keep them out of any
  externally shared URL.

---

## Part 7 — Plan: provenance + deep links (smallest useful version)

Ordered, each a separately reviewable change:

1. **Resolve links' host problem first (Part 0)** — wildcard DNS → Cilium LB;
   delete `hosts-entries.sh`. *Without this, links don't open.*
2. **Server: return the evidence trail.** `/investigate` and `/ui/message`
   already compute `facts`; add a `facts` (or `evidence`) field to the response
   carrying `tool,args,ok,ms,at,source,reproduce`. *(No prompt change, no tokens.)*
3. **Server: build `reproduce.cli` per tool** from a static map
   (`get_pods`→`kubectl get pods -n {namespace}`, …) — pure templating of args.
4. **Grafana links:** resolve the Prometheus datasource **UID once at startup**
   (`GET /api/datasources`), then build the Explore URL for `query_prometheus`
   facts. Grafana-only.
5. **Hubble links:** for k8s-tool facts, embed `?namespace={namespace}`; no flow
   links. **Flow facts get a copyable `hubble observe --namespace …` instead.**
6. **UI:** render the collapsed evidence trail (Part 5) with copy buttons and the
   two link kinds. Reuse the existing `<details>`/chip styling in `ui.py`.

Deliberately out of scope: auth proxy, dashboard generation, VMUI exposure,
per-fact diffs.

---

## Part 8 — Plan: the cartography tool

Goal: a **read-only, bounded** tool giving the agent a high-level cluster/app
topology. (Detail in `research/reasoner-and-topology.md`, §Hypothesis 2.)

**Design (unchanged from that investigation):**

1. **Data source:** Kubernetes API only, for v1 — `ownerReferences` (ownership
   edges), selectors/EndpointSlices (route edges), services/ingress, node
   placement, status. **No** OTel (not deployed); **no** Hubble relay edges yet.
2. **One tool**, level-1: `get_topology(namespace?: str, depth?: int)` returning a
   **bounded** graph `{scope, nodes[], edges[], rollup{}}`.
3. **Size to the pipeline budget:** it is one *fact*, so ≤ `PER_FACT_CHARS=600`
   typical → collapse large scopes to `(kind,count)`, cap nodes/edges, honor an
   explicit `scope`.
4. **Register** in `tools.REGISTRY` (level 1) → `pipeline.READ_TOOLS` picks it up
   automatically; add a `_BUNDLES` entry for "topology / how does traffic flow"
   so common asks cost **0 plan LLM calls**.
5. **RBAC:** the existing read-only ClusterRole already covers
   get/list/watch; add nothing.
6. **Provenance for free:** it flows through the same `facts`/evidence mechanism
   (Part 1) → `reproduce.cli` = the `kubectl` equivalent; graph facts typically
   have **no deep link** (class: reproducible).

**Steps:**

1. `get_topology` in `apps/assistant/tools.py` (k8s API, bounded graph).
2. Wire the `_BUNDLES` entry in `pipeline.py`.
3. Add `reproduce` mapping for it (Part 7 step 3).
4. Later (separate): add Hubble **relay** edges and/or a Neo4j-backed variant —
   only if the k8s-API graph proves insufficient.

**Not** in v1: per-flow edges, the Cartography/Neo4j graph as the source, a UI
graph renderer.

---

## Recommendation summary

1. **Schema:** the per-fact `{tool,args,ok,ms,at,source,reproduce,links}` object.
2. **Reproducible commands:** all tools except `search_knowledge`/`remember`/`recall`.
3. **Grafana deep links:** yes — `query_prometheus` (Explore panes URL, v13.2.3).
4. **Hubble deep links:** namespace-scoped only; flows via `hubble observe`.
5. **Outside the LLM context:** everything except the existing summary/excerpts.
6. **UI:** a collapsed, copyable evidence trail.
7. **Smallest version:** Part 7 steps 1–6, and Part 8's `get_topology`.

Prerequisite to all of it: **Part 0** — real DNS + Cilium LB, `/etc/hosts` gone.
