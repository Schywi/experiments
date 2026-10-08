"""Evidence provenance for the UI — deliberately kept OUT of the LLM context.

Each pipeline fact is `{tool, args, ok, evidence, ms}`. This module enriches a
fact with the metadata a human needs to *reproduce* what the agent saw:

    at         UTC timestamp
    source     which system answered (kubernetes / victoriametrics / ...)
    reproduce  a copyable command or PromQL
    links      resolvable deep links (Grafana direct; Hubble namespace-only)

Nothing here is passed to plan/correlate — it rides alongside, so it costs the
model zero tokens. Hosts come from env and should be LAN DNS names, e.g.
http://grafana.home.arpa and http://hubble.home.arpa.
"""

import json
import os
import shlex
import urllib.parse
from datetime import datetime, timezone

GRAFANA_URL = os.environ.get("GRAFANA_URL", "").rstrip("/")
HUBBLE_URL = os.environ.get("HUBBLE_URL", "").rstrip("/")
GRAFANA_DATASOURCE_UID = os.environ.get("GRAFANA_DATASOURCE_UID", "")
GRAFANA_DATASOURCE_NAME = os.environ.get("GRAFANA_DATASOURCE_NAME", "victoriametrics")

_SOURCE = {
    "get_pods": "kubernetes",
    "get_pod_logs": "kubernetes",
    "get_events": "kubernetes",
    "search_logs": "kubernetes",
    "get_topology": "kubernetes",
    "query_prometheus": "victoriametrics",
    "search_knowledge": "corpus",
    "remember": "memory",
    "recall": "memory",
}


def _kubectl(tool: str, args: dict):
    ns = args.get("namespace")
    if tool == "get_pods":
        return f"kubectl get pods{(' -n ' + ns) if ns else ''}"
    if tool == "get_events":
        return f"kubectl get events{(' -n ' + ns) if ns else ''}"
    if tool == "get_pod_logs":
        return (f"kubectl logs {args.get('pod', '?')} -n {ns or 'default'}"
                f" --tail {args.get('tail_lines', 100)}")
    if tool == "search_logs":
        return (f"kubectl logs -n {ns or 'default'} --all-containers"
                f" | grep -i {shlex.quote(str(args.get('contains', '')))}")
    return None


def _grafana_explore(promql: str):
    """A Grafana Explore deep link (panes schema, Grafana 9+), or None."""
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


def _hubble_link(args: dict):
    """Hubble UI is namespace-scoped only (verified: no flow-query deep link)."""
    ns = args.get("namespace")
    if not (HUBBLE_URL and ns):
        return None
    return f"{HUBBLE_URL}/?namespace={urllib.parse.quote(str(ns))}"


def enrich(fact: dict) -> dict:
    out = dict(fact or {})
    tool = out.get("tool")
    args = out.get("args") or {}
    if not isinstance(args, dict):
        args = {}

    out["at"] = datetime.now(timezone.utc).isoformat(timespec="seconds")
    out["source"] = _SOURCE.get(tool, "unknown")

    reproduce = {}
    if tool == "query_prometheus" and args.get("query"):
        reproduce["promql"] = args["query"]
    cli = _kubectl(tool, args)
    if cli:
        reproduce["cli"] = cli
    if reproduce:
        out["reproduce"] = reproduce

    links = {}
    if tool == "query_prometheus":
        grafana = _grafana_explore(args.get("query", ""))
        if grafana:
            links["grafana"] = grafana
    hubble = _hubble_link(args)
    if hubble:
        links["hubble"] = hubble
    if links:
        out["links"] = links

    return out


def enrich_all(facts) -> list:
    return [enrich(f) for f in (facts or [])]
