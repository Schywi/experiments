# Feature: Evidence provenance & deep links

> Status: **implemented and applied** on the cluster (branch
> `native-k3s-gpu-models`). Grounded against the running assistant.

## What it does

For every tool the investigation pipeline runs, the answer carries **human
reproducibility metadata**: the tool, its exact arguments, a timestamp, which
system answered, a **copyable command/PromQL**, and — where the source supports
it — a **one-click deep link** into Grafana or Hubble.

Goal, verbatim: the agent tells you what it found, shows you where the evidence
came from, and gives you a one-click or copyable way to independently verify it.

## The contract

`POST /investigate` returns `facts[]`; each fact is enriched:

```json
{
  "tool": "query_prometheus",
  "args": {"query": "sum(rate(hubble_drop_total[5m])) by (reason))"},
  "ok": true,
  "ms": 79.9,
  "at": "2026-10-08T19:58:15+00:00",
  "source": "victoriametrics",
  "reproduce": {"promql": "sum(rate(hubble_drop_total[5m])) by (reason))"},
  "links": {"grafana": "http://grafana.home.arpa/explore?schemaVersion=1&panes=…"}
}
```

| Field | Meaning |
|---|---|
| `at` | UTC timestamp of the call |
| `source` | `kubernetes` \| `victoriametrics` \| `corpus` \| `memory` |
| `reproduce.cli` | the exact `kubectl` command (k8s tools) |
| `reproduce.promql` | the exact PromQL (`query_prometheus`) |
| `links.grafana` | a direct Grafana **Explore** URL (`query_prometheus`) |
| `links.hubble` | a **namespace-scoped** Hubble UI link (facts carrying a namespace) |

Missing pieces are simply absent (no `links` on a fact with no namespace, no
`reproduce` for memory/corpus tools).

## Deep-link rules (link vs copyable command)

| Tool | Reproduce with | Deep link |
|---|---|---|
| `query_prometheus` | PromQL + `curl /api/v1/query` | **Grafana Explore — direct** |
| `get_pods` | `kubectl get pods [-n NS]` | Hubble `?namespace=NS` (if NS given) |
| `get_pod_logs` | `kubectl logs POD -n NS --tail N` | Hubble (if NS) |
| `get_events` | `kubectl get events [-n NS]` | Hubble (if NS) |
| `search_logs` | `kubectl logs -n NS --all-containers \| grep -i X` | Hubble (if NS) |
| `search_knowledge` / `remember` / `recall` | not externally reproducible | — |
| Hubble **flows** | `hubble observe --namespace NS` | **none** — the deployed Hubble UI is namespace-scoped only |

## How it is implemented

| File | Role |
|---|---|
| `apps/assistant/provenance.py` | `enrich(fact)` / `enrich_all(facts)` — adds `at`, `source`, `reproduce`, `links`. Pure functions; no I/O except env. |
| `apps/assistant/server.py` | `facts=provenance.enrich_all(out["facts"])` on `/investigate` and `/ui/message`. |
| `apps/assistant/ui.py` | renders the collapsed **`Evidence (N)`** trail: `tool · args · ms`, the copyable command, `Open in Grafana`/`Open in Hubble`, and a copy-to-clipboard handler. |
| `apps/assistant/chart/` | Deployment env `GRAFANA_URL`, `HUBBLE_URL`, `GRAFANA_DATASOURCE_UID`, `GRAFANA_DATASOURCE_NAME`. |

### Grafana URL shape

Grafana 9+ **Explore panes** schema (verified against the deployed
**Grafana 13.2.3**), URL-encoded:

```
/explore?schemaVersion=1&panes={"a":{
  "datasource":"<uid>",
  "queries":[{"refId":"A","expr":"<promql>",
              "datasource":{"type":"prometheus","uid":"<uid>"}}],
  "range":{"from":"now-1h","to":"now"}}}
```

`<uid>` is `GRAFANA_DATASOURCE_UID`, else `GRAFANA_DATASOURCE_NAME`
(default `victoriametrics`).

### Hubble

Namespace-scoped only: `<HUBBLE_URL>/?namespace=NS`. Verified against the
deployed **Hubble UI v0.13.5** — its bundle reads URL params and honours
`namespace=`, but there is **no** flow-query deep link, so exact flows get a
copyable `hubble observe` instead.

## The LLM never sees it

The metadata is returned on a **separate field** and rendered by the UI. The
`plan`/`correlate` prompts keep their ~2600-char evidence budget — provenance
adds **zero tokens** and the model cannot mint a link.

## Configuration

`apps/assistant/chart/values.yaml`:

```yaml
provenance:
  grafanaUrl: "http://grafana.home.arpa"
  hubbleUrl: "http://hubble.home.arpa"
  grafanaDatasourceUid: ""            # optional; else match grafanaDatasourceName
  grafanaDatasourceName: "victoriametrics"
```

The hosts must **resolve** — see `config/dns/` (LAN resolver answers
`*.home.arpa` → the Cilium ingress LB). If they don't resolve, the links won't
open for a human.

## Security

- URLs are generated **server-side** from trusted `facts`; the model output is
  never used to build a link.
- Only **resolvable** hosts are emitted (`*.home.arpa`, the LB) — never
  `*.svc.cluster.local`.
- In a hostile deployment, proxy observability through an authenticated
  assistant-origin path rather than linking internal endpoints directly.

## Verify

```bash
# JSON: the provenance on each fact
curl -s -X POST "http://<assistant-lb>:8080/investigate" \
  -H 'content-type: application/json' \
  -d '{"message":"are there any dropped packets?"}' \
  | python3 -m json.tool | grep -A4 reproduce

# UI: open http://assistant.home.arpa/ , ask, expand "Evidence (N)"
```

Expected: `query_prometheus` facts carry `reproduce.promql` and
`links.grafana`; k8s facts carry `reproduce.cli`.

## Limits

- Grafana link needs the datasource UID/name to match; a wrong value yields a
  link that opens Explore but shows "no data".
- No flow-level Hubble link (by design — see Deep-link rules).
- Reference `localhost`/`home.arpa` hosts require the LAN resolver; without it,
  fall back to the `kubectl`/`hubble` copyable commands, which always work.
