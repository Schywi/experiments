# RFC — Instrumentation coverage matrix

| | |
|---|---|
| Status | Draft for review |
| Date | 2026-10-28 |
| Scope | `config/victoriametrics`, `config/grafana`, `config/helm/cilium`, `config/vector`, `config/gpu-exp`, `apps/assistant/provenance.py` |
| Measured against | The live cluster reachable from this workstation: VictoriaMetrics `192.168.0.246`, Grafana `192.168.0.245`, Hubble UI `192.168.0.241`, sandbox console `192.168.0.250` |
| Related | [`test-matrix.md`](test-matrix.md) (which links each answer *should* carry), [`evidence-provenance.md`](evidence-provenance.md) (how a link is built) |

Every number below was read from the running cluster on 2026-10-28. Section 7
carries the commands.

---

## 1. Summary

The assistant attaches a Grafana deep link to any successful `query_prometheus`
fact. The link is assembled in Python from the PromQL string and the
`GRAFANA_URL` environment variable, so it renders in Grafana whether or not the
metric it names has ever been written. An empty result set is a successful
query, so a question about an uninstrumented namespace produces a page that
reads "No data", which a reader interprets as a measurement of zero.

Four facts about the current cluster:

1. Five scrape jobs run, all healthy: four Cilium/Hubble components and the CPU
   assistant. No other workload is scraped.
2. The 364 metric names in VictoriaMetrics split as 269 `hubble_*`/`cilium_*`,
   32 `assistant_*`, 47 Go/Python runtime, and 16 scrape self-metrics. No
   container, node, or GPU metric exists.
3. `hubble_*` aggregate metrics carry `protocol`, `type`, `verdict`, `reason`,
   `node`, `job` and `instance`. They carry no namespace and no workload label,
   so every namespace-scoped PromQL returns an empty vector.
4. Three of the six shipped dashboards query labels that do not exist, and a
   fourth has panels that do. Grafana's home dashboard is one of them.

This document records what is measured, what is not, and the changes that
close the gap.

## 2. Why the deep link misreports an empty result

### 2.1 The link is constructed locally

`apps/assistant/provenance.py:63-75`:

```python
def _grafana_explore(promql: str):
    if not (GRAFANA_URL and promql):
        return None
    ds = GRAFANA_DATASOURCE_UID or GRAFANA_DATASOURCE_NAME
    panes = {"a": {
        "datasource": ds,
        "queries": [{"refId": "A", "expr": promql,
                     "datasource": {"type": "prometheus", "uid": ds}}],
        "range": {"from": "now-1h", "to": "now"},
    }}
    query = urllib.parse.urlencode({"schemaVersion": "1", "panes": json.dumps(panes)})
    return f"{GRAFANA_URL}/explore?{query}"
```

No request reaches Grafana at assembly time. The URL is a string built from the
PromQL text. Grafana renders `/explore` for any syntactically valid `panes`
parameter, and an empty series renders as an empty graph.

### 2.2 The guard tests the wrong condition

`apps/assistant/provenance.py:153-156`:

```python
if tool == "query_prometheus" and out.get("ok") and intent == "prometheus":
    grafana = _grafana_explore(args.get("query", ""))
    if grafana:
        links["grafana"] = grafana
```

`ok` is set by `observability.query_prometheus`, which returns
`ok=True` whenever the HTTP call to `/api/v1/query` resolves, at
`apps/assistant/observability.py:48-53`:

```python
return {
    "query": query,
    "count": len(results),
    "truncated": len(results) > VM_MAX_SERIES,
    "series": series,
}
```

`count` is available and unused by the guard. A result of `[]` yields
`count=0` and `ok=True`.

### 2.3 Measured behaviour

Three instant queries against VictoriaMetrics on 2026-10-28:

| Query | `data.result` |
|---|---|
| `sum(rate(hubble_drop_total{destination_namespace="worm-lab"}[5m]))` | `[]` |
| `sum(rate(hubble_drop_total{source_workload=~"worm-.*"}[5m]))` | `[]` |
| `sum by (reason) (rate(hubble_drop_total[5m]))` | 4 series: `POLICY_DENIED`, `STALE_OR_UNROUTABLE_IP`, `UNSUPPORTED_L2_PROTOCOL`, `UNSUPPORTED_L3_PROTOCOL` |

The first two produce a Grafana button with identical confidence to the third.

### 2.4 Why namespace scoping cannot work

A live series from `hubble_flows_processed_total`:

```
hubble_flows_processed_total{instance="192.168.0.28:9965", job="cilium-hubble",
  node="fedora", protocol="TCP", type="Drop", verdict="DROPPED"}
```

A live series from `hubble_drop_total`:

```
hubble_drop_total{instance="192.168.0.28:9965", job="cilium-hubble",
  node="fedora", protocol="TCP", reason="POLICY_DENIED"}
```

`config/helm/cilium/values.yaml:59` sets
`httpV2:exemplars=true;labelsContext=source_ip,source_namespace,source_workload,destination_ip,destination_namespace,destination_workload,traffic_direction`.
That option applies to the `httpV2` metric family only. It does not add
namespace or workload labels to `flow`, `drop`, `tcp`, `icmp`, or
`port-distribution`.

The aggregation is per-node, so the finest granularity available today is
"drop reason, per node, per protocol".

## 3. Measured coverage

### 3.1 Scrape targets

`GET http://192.168.0.246/api/v1/targets` on 2026-10-28, 5 targets, 5 healthy:

| Job | Scrape address | Serves |
|---|---|---|
| `cilium-agent` | `192.168.0.28:9962` | `cilium_*` datapath, policy, node health |
| `cilium-hubble` | `192.168.0.28:9965` | `hubble_flow`, `hubble_drop`, `hubble_tcp_flags`, `hubble_icmp`, `hubble_port_distribution`, `hubble_lost_events` |
| `cilium-operator` | `192.168.0.28:9963` | `cilium_operator_*` |
| `hubble-relay` | `10.42.0.67:9966` | `grpc_server_*` |
| `assistant` | `10.42.0.244:8080` | `assistant_llm_*`, `assistant_tool_*`, `assistant_concurrency_*` |

All five are configured in `config/victoriametrics/templates/configmap-scrape.yaml`.

### 3.2 Families declared in the agent configuration that are never exported

`kubectl -n kube-system get cm cilium-config` reports:

```
"enable-l7-proxy":"true"
"enable-envoy-config":"true"
"hubble-metrics":"dns drop tcp flow port-distribution icmp httpV2:exemplars=true;labelsContext=..."
```

`GET http://192.168.0.28:9965/metrics` returns these families:

```
hubble_drop_total
hubble_flows_processed_total
hubble_icmp_total
hubble_lost_events_total
hubble_metrics_http_handler_request_duration_seconds
hubble_metrics_http_handler_requests_total
hubble_port_distribution_total
hubble_tcp_flags_total
```

`hubble_dns_queries_total` and `hubble_http_requests_total` are absent from the
exporter itself, so this is not a scrape or relabel problem. Both are counted
only when a matching event is observed, and no L7 or DNS policy has produced
one on this cluster.

The exporter also reports its own loss:

| `hubble_lost_events_total{source=...}` | Value |
|---|---|
| `hubble_ring_buffer` | 8090 |
| `observer_events_queue` | 7060 |
| `perf_event_ring_buffer` | 5 |

Even the flow, drop, TCP and ICMP series are an incomplete record of traffic.

### 3.3 Dashboards against their backing metrics

Six dashboards are served from `config/grafana/dashboards/**` via the
`configmap-dashboards.yaml` ConfigMap. `config/grafana/values.yaml:32-34` sets
`homeDashboard.path` to `hubble-dashboard.json`.

| Dashboard | uid | Queries on | State |
|---|---|---|---|
| Cilium Metrics | `vtuWtdumz` | `cilium_*`, no namespace labels | Backed |
| Cilium Operator | `1GC0TT4Wz` | `cilium_operator_*` | Backed |
| Hubble Metrics and Monitoring | `5HftnJAWz` | mix, includes `hubble_dns_queries_total` and `hubble_http_requests_total` | Partly backed; the DNS and HTTP panels are empty. This is the landing page |
| Hubble / Network Overview (Namespace) | `nlsO8tYVz` | `source_namespace`, `destination_namespace` | No data |
| Hubble / DNS Overview (Namespace) | `_f0DUpY4k` | `source_namespace`, `destination_namespace`, `hubble_dns_queries_total` | No data |
| Hubble L7 HTTP Metrics by Workload | `3g264CZVz` | `source_namespace`, `destination_namespace`, `hubble_http_requests_total` | No data |

`source_namespace` and `destination_namespace` do not appear in any series in
VictoriaMetrics. The namespace dashboards cannot populate from any configuration
short of changing what Cilium exports.

### 3.4 Per-world coverage

Twenty-two namespaces are `Active`. Workload state read from
`kubectl get deploy,sts,ds,cronjob -A`; pods from `kubectl get pods -A`.

| World (namespace) | Workloads live | Metrics | Logs | Traces | Dashboard | Link kinds it can emit |
|---|---|---|---|---|---|---|
| `kube-system` (Cilium, Hubble) | `cilium` DS, `cilium-envoy` DS, operator, relay, UI, coredns, metrics-server | `hubble_*`/`cilium_*`, cluster-wide only | none | none | 2 backed, 1 partial | `grafana` yes, unscoped |
| `observability` (Grafana, VictoriaMetrics) | 1/1, 1/1 | self only: `up`, `scrape_*` | none | none | none | n/a |
| `assistant` (CPU) | `assistant` 1/1 | 32 `assistant_*` | none | none | none | `grafana`, `hubble`, `sandbox` |
| `assistant` (`assistant-e4-gpu`) | 1/1 | **none** — see D1 | none | none | none | `grafana`, `hubble`, `sandbox`, `neo4j` |
| `assistant` (`assistant-e4-cpu`, `assistant-laya`) | 0/0 | n/a | n/a | n/a | none | n/a |
| `gpu-exp` (`llm-gpu`) | 1/1 | none | none | none | none | none |
| `gpu` (`gpu-device-plugin`) | 1 DS pod | none, container declares no port | none | none | none | none |
| `models` (`llm`, `kokoro`; `laya` 0/0) | 1/1, 1/1 | none | none | none | none | none |
| `fuwa` (`fuwa`, `loki`, `tempo`, `otel-collector`) | 1/1 x4 | `/metrics` returns 200 on `192.168.0.244`, not scraped | the only Loki stream | the only Tempo service | none | none |
| `sandbox-console` | 1/1 | `/metrics` returns 404 | none | none | none | `sandbox` yes, partial — see D5 |
| `sandbox-demo` (browser pools) | 10 `Sandbox` CRs, all Ready | none | none | none | none | none |
| `agent-sandbox-system` | controller, router 1/1 | none | none | none | none | none |
| `cartography` (`cartography-neo4j`) | 0/0; CronJobs failing | none | none | none | none | `neo4j` returns connection refused — see D4 |
| `worm-lab` (`worm-controller`, `worm-worker`, `regression`, `vector`) | all 0/0 | none | none — the Vector ConfigMap is dormant | none | none | `grafana` yes, empty |
| `argocd` | 1/1 x6 | none | none | none | none | none |
| `kargo`, `kargo-pilot-{prod,staging}`, `kargo-{shared,system}-resources` | 1/1 each | none | none | none | none | none |
| `registry` | 1/1 | none | none | none | none | none |
| `default`, `pilot`, `cilium-secrets`, `kube-*` | minimal | none | none | none | none | none |

Absent cluster-wide: `kube-state-metrics`, cAdvisor, node-exporter, DCGM. No
`container_*`, `kube_*` (state), `node_*`, or `DCGM_*` series exist.
`metrics-server` runs at `kube-system/metrics-server` and serves the
`metrics.k8s.io` API, which VictoriaMetrics does not consume.

### 3.5 Logs and traces

Grafana carries three datasources: `VictoriaMetrics` (`prometheus`,
`http://victoriametrics:8428`), `Loki` (`http://loki.fuwa.svc.cluster.local:3100`),
`Tempo` (`http://tempo.fuwa.svc.cluster.local:3200`).

Loki: `GET /loki/api/v1/labels` returns `["service_name"]`. The only stream that
matches `{service_name=~".+"}` is `service_name="fuwa"`.

Tempo: `GET /api/search/tags` returns `http.method`, `http.route`,
`http.status_code`, `service.name`. Every trace sampled has
`rootServiceName="fuwa"`, `rootTraceName="GET /"`.

No dashboard in Grafana queries Loki or Tempo, so both datasources are
reachable and unused.

## 4. Defects

**D1 — `assistant-e4-gpu` metrics are dropped by the scrape selector.**
The pod `assistant-e4-gpu-7ddf5b9c94-rcxbx` carries the single label
`app=assistant-e4-gpu` and serves `/metrics` with HTTP 200 on `10.42.0.198:8080`.
The job in `configmap-scrape.yaml:80-83` keeps
`__meta_kubernetes_pod_label_app_kubernetes_io_name` matching `assistant`, which
that pod does not have. The GPU variant is the subject of the E4 A/B comparison
and none of its `assistant_llm_*` series reach VictoriaMetrics.

**D2 — The deep link does not distinguish an empty result from a measurement.**
`provenance.py:153` gates on `ok`, not on `count`. Combined with D3 this makes
every namespace question look like a zero.

**D3 — No namespace or workload dimension exists on any `hubble_*` metric.**
Section 2.4. Every namespace-scoped PromQL returns `[]`. The assistant's
PromQL writer is told which metric names exist
(`pipeline.py:_write_promql`) but is not told that `source_namespace` is not a
label, so a question naming a namespace reliably produces a selector that
matches nothing.

**D4 — Cartography is down and the `neo4j` link points at it.**
`cartography-neo4j` is 0/0, all CronJobs in `cartography` are failing.
`config/gpu-exp/assistant-e4-gpu.yaml:90-91` sets `NEO4J_UI_URL` to
`http://192.168.0.242/browser/?...`; `192.168.0.242:7687` and `:80` both refuse
connections. `get_topology_snapshot` cannot return evidence, and the `neo4j`
link this module attaches would open a dead host.

**D5 — The `sandbox` deep link resolves for 2 of 10 live sandboxes.**
Ten `Sandbox` objects exist in `sandbox-demo`, all Ready.
`GET http://192.168.0.250/api/sandboxes` returns 2 tiles
(`browser-pool-22grt`, `browser-pool-ww79w`, both carrying a `claim`).
`GET /s/browser-pool-ww79w` and `/s/browser-pool-22grt` return 200;
`/s/browser-pool-4zcv6` returns 404 although that `Sandbox` is Ready. The
console lists claimed sandboxes only, so a link to an unclaimed warm-pool member
is a 404. The assistant obtains the id from a `sandbox_create` fact, so links it
produces do resolve; ids copied from `kubectl get sandboxes` do not.

**D6 — `worm-lab` is scaled to zero and is used as the prompt's example.**
`apps/assistant/intent.py:43` reads:

```python
"Example: 'any dropped packets in worm-lab, and a screenshot of browser-pool-x' returns "
```

`research/features/assistant/evaluator.md:21,24,38,52,108`,
`research/features/assistant/test-questions.md:20` and
`research/features/assistant/recipes.md:8` do the same. All four `worm-lab`
deployments are 0/0. The evaluator is trained by example to answer about a
namespace that produces no data.

**D7 — Three dashboards query labels that have never existed here.**
Section 3.3. Two of the three namespace dashboards are among them.

**D8 — The GPU assistant's `UI_LINKS` omits the consoles the shell exposes.**
`config/gpu-exp/assistant-e4-gpu.yaml:95-96` lists Grafana, Hubble UI and
VictoriaMetrics. The sandbox console at `192.168.0.250`, Argo CD at
`192.168.0.247` and Kargo are absent, so the header of that deployment links to
three panels while `fuwa`'s header links to more.

## 5. Proposed changes

Ordered by ratio of information gained to effort. Each item is independently
applicable.

### P1 — Scrape `assistant-e4-gpu`

Add the conventional labels to the pod template in
`config/gpu-exp/assistant-e4-gpu.yaml`, then distinguish the two variants with a
target label so `up{job="assistant"}` returns two series.

Pod template:

```yaml
    metadata:
      labels:
        app: assistant-e4-gpu
        app.kubernetes.io/name: assistant
        app.kubernetes.io/instance: assistant-e4-gpu
```

`config/victoriametrics/templates/configmap-scrape.yaml`, inside the
`assistant` job's `relabel_configs`, after the existing `keep` rule:

```yaml
          - source_labels: [__meta_kubernetes_pod_label_app_kubernetes_io_instance]
            target_label: variant
```

The existing `keep` rule then matches both pods and the `variant` label
separates the CPU and GPU variants in every panel.

Verify: `up{job="assistant"}` returns two series with `variant="assistant"` and
`variant="assistant-e4-gpu"`.

### P2 — Make the deep-link guard data-aware

In `apps/assistant/provenance.py`, gate the link on the fact's own count and
record the absence explicitly:

```python
    if tool == "query_prometheus" and out.get("ok") and intent == "prometheus":
        if out.get("count"):
            grafana = _grafana_explore(args.get("query", ""))
            if grafana:
                links["grafana"] = grafana
        else:
            out["empty_result"] = True
```

`enrich` already receives `count` because `observability.query_prometheus`
returns it. A UI that shows `empty_result` can say the query matched no series
instead of offering a graph.

Two tests in `apps/assistant/test_guardrails.py` need the fixture updated:
`_prom()` at line 25-26 builds a fact with no `count`, and
`test_prometheus_intent_ok_gets_grafana` at line 31-34 asserts a `grafana` link
for it. Give `_prom` a `count` keyword and add a case asserting that
`count=0` yields `empty_result` and no link.


### P3 — Add `kube-state-metrics` and cAdvisor

`kube-state-metrics` supplies object state (`kube_deployment_status_replicas`,
`kube_pod_status_phase`, `kube_pod_container_status_restarts_total`) and cAdvisor
supplies `container_cpu_usage_seconds_total`,
`container_memory_working_set_bytes`. Together they turn "does `worm-lab` exist
and is it healthy" into a query with a non-empty answer for every namespace in
the matrix at once, and give the assistant a metric family that does carry
`namespace` and `pod` labels.

Both are new scrape jobs in `configmap-scrape.yaml` and a new chart under
`config/`. Neither needs a Prometheus Operator.

### P4 — Choose a per-world instrumentation level

The matrix in 3.4 lists every world with nothing. Each needs one decision:

| World | Options |
|---|---|
| `gpu-exp/llm-gpu`, `models/llm` | llama.cpp and vLLM both serve `/metrics`; scrape it directly, or enable DCGM on the GPU node for utilisation, VRAM and temperature |
| `gpu/gpu-device-plugin` | no metrics port today; DCGM exporter on the node covers the same ground with less code |
| `sandbox-console` | `/metrics` returns 404; a small `/metrics` with claim, create, open and shot counters would make D5's tile count observable |
| `fuwa` | already exports on `192.168.0.244`; add a scrape job and a dashboard for the Loki and Tempo series that already exist |
| `assistant-e4-gpu` | P1 |
| `cartography` | repair or remove; D4 |
| `worm-lab` | keep at zero and delete from the prompts (P5), or bring up and scrape |

### P5 — Remove `worm-lab` from the prompts and test docs

Replace the `worm-lab` example in `apps/assistant/intent.py:43` and the four
docs listed in D6 with a namespace that is live and instrumented. `sandbox-demo`
is live but has no metrics; `assistant` has metrics.

### P6 — Reconcile the dashboard set with what Cilium exports

Two options, and the choice belongs to whoever owns the observability target:

- Enable the L7 and DNS sources so the families materialise, by adding an
  L7 rule to a policy that matches real traffic and enabling DNS visibility on
  the namespaces that matter. The three dashboards then populate.
- Or drop `dns` and `httpV2` from `config/helm/cilium/values.yaml:52-59`, remove
  the three namespace dashboards from `configmap-dashboards.yaml`, and strip the
  DNS and HTTP panels from `hubble-dashboard.json`. Three of six dashboards
  become two of three, and Grafana's landing page has no empty panel.

Leaving the declaration and the export out of step is what produces D7.

### P7 — Record the lost-event rate

`hubble_lost_events_total` is already scraped. A panel for
`rate(hubble_lost_events_total[5m])` on the Cilium dashboard states the
sampling loss the flow numbers carry (8090 and 7060 events at time of
measurement). It costs one panel and prevents the flow counts being read as
complete.

## 6. Non-goals

- Replacing Grafana, Hubble UI or vmui. The assistant links to them.
- A Prometheus Operator. `config/victoriametrics` scrapes by pod label and that
  stays.
- Changing the provenance contract in
  [`evidence-provenance.md`](evidence-provenance.md). P2 adds one field to a
  fact and leaves `links` the shape it already has.
- Instrumenting `worm-lab`. It is scaled to zero and P5 removes the prompts that
  assume otherwise.

## 7. Reproducing the measurements

All commands are read-only. `$VM=192.168.0.246`, `$G=192.168.0.245`.

Scrape health:

```bash
curl -s "$VM/api/v1/targets" \
| jq -r '.data.activeTargets[] | [.labels.job, .health, .scrapeUrl] | @tsv'
```

Metric-name inventory:

```bash
curl -s "$VM/api/v1/label/__name__/values" \
| jq -r '[.data[] | select(type=="string")] as $n
  | "total=\($n|length)
     cilium_hubble=\([$n[]|select(startswith("hubble_") or startswith("cilium_"))]|length)
     assistant=\([$n[]|select(startswith("assistant_"))]|length)"'
```

Labels actually present on a Hubble series:

```bash
curl -s --get --data-urlencode 'query=hubble_flows_processed_total' \
  "$VM/api/v1/query" \
| jq -r '.data.result[0].metric | to_entries | map("\(.key)=\(.value)") | join(" ")'
```

Which families the agent exports, independent of the scrape config:

```bash
curl -s 'http://192.168.0.28:9965/metrics' | grep '^# TYPE'
```

The empty-result demonstration:

```bash
for q in 'sum(rate(hubble_drop_total{destination_namespace="worm-lab"}[5m]))' \
         'sum by (reason) (rate(hubble_drop_total[5m]))'; do
  printf '%s -> ' "$q"
  curl -s --get --data-urlencode "query=$q" "$VM/api/v1/query" | jq -c '.data.result'
done
```

Datasources and dashboards:

```bash
curl -s "$G/api/datasources" | jq -r '.[] | [.name,.uid,.type,.url] | @tsv'
curl -s "$G/api/search?type=dash-db&limit=100" | jq -r '.[] | [.uid,.title] | @tsv'
```

Loki and Tempo contents:

```bash
curl -s "$G/api/datasources/proxy/uid/loki/loki/api/v1/labels"
curl -s --get --data-urlencode 'match[]={service_name=~".+"}' \
  "$G/api/datasources/proxy/uid/loki/loki/api/v1/series"
curl -s "$G/api/datasources/proxy/uid/tempo/api/search/tags"
```

Sandbox tile feed against live objects:

```bash
kubectl -n sandbox-demo get sandboxes.agents.x-k8s.io --no-headers
curl -s http://192.168.0.250/api/sandboxes | jq -r '.tiles[] | [.id, .phase] | @tsv'
```

Workload state per namespace:

```bash
kubectl get deploy,sts,ds,cronjob -A
```
