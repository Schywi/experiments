"""Evidence provenance for the UI — kept OUT of the LLM context.

A fact is enriched with what a human needs to reproduce it and, **only when the
question needs one**, a single deep link. Each link kind fires for exactly one
intent, so an answer never carries unrelated buttons:

    intent "prometheus"  -> grafana   (query_prometheus, only if ok == true)
    intent "kube"        -> hubble    (a fact that carries args.namespace)
    intent "cartography" -> neo4j     (get_topology_snapshot, only if env is set)
    intent "sandbox"     -> sandbox   (a fact carrying a sandbox id)
    anything else        -> no links

Hosts come from env; an unset env drops that kind. No host is written here.
"""

import json
import os
import re
import shlex
import urllib.parse
from datetime import datetime, timezone

from sandbox import CONSOLE_URL as SANDBOX_URL

GRAFANA_URL = os.environ.get("GRAFANA_URL", "").rstrip("/")
HUBBLE_URL = os.environ.get("HUBBLE_URL", "").rstrip("/")
GRAFANA_DATASOURCE_UID = os.environ.get("GRAFANA_DATASOURCE_UID", "")
GRAFANA_DATASOURCE_NAME = os.environ.get("GRAFANA_DATASOURCE_NAME", "victoriametrics")
NEO4J_UI = os.environ.get("NEO4J_UI_URL", "").rstrip("/")

_SOURCE = {
    "get_pods": "kubernetes",
    "get_pod_logs": "kubernetes",
    "get_events": "kubernetes",
    "search_logs": "kubernetes",
    "get_topology": "kubernetes",
    "get_topology_snapshot": "cartography",
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
    if tool == "get_topology":
        return (f"kubectl get deploy,rs,pod,svc,ing -n {ns}" if ns
                else "kubectl get deploy,rs,pod,svc,ing -A")
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


def _hubble_link(namespace):
    """Hubble is Cilium's UI; namespace-scoped only (no flow-query route)."""
    if not (HUBBLE_URL and namespace):
        return None
    return f"{HUBBLE_URL}/?namespace={urllib.parse.quote(str(namespace))}"


_TILE_ID = re.compile(r'"(?:sandbox|sandbox_id|id)"\s*:\s*"([A-Za-z0-9][A-Za-z0-9._-]{2,})"')


def _sandbox_id(args: dict, evidence: str):
    """The sandbox a fact is about: from its args, else from the tool's tile."""
    for key in ("sandbox_id", "sandbox", "id"):
        value = args.get(key)
        if isinstance(value, str) and value:
            return value
    match = _TILE_ID.search(evidence or "")
    return match.group(1) if match else None


def _sandbox_link(sandbox_id: str):
    """The console's deep link: a full document with that sandbox dialog open."""
    if not SANDBOX_URL:
        return None
    return f"{SANDBOX_URL}/s/{urllib.parse.quote(sandbox_id)}"


def enrich(fact: dict, intent: str = "general") -> dict:
    out = dict(fact or {})
    tool = out.get("tool")
    args = out.get("args") or {}
    if not isinstance(args, dict):
        args = {}
    evidence = out.get("evidence") or ""

    out["at"] = datetime.now(timezone.utc).isoformat(timespec="seconds")
    out["source"] = _SOURCE.get(tool, "unknown")

    if tool == "get_topology_snapshot":
        # The snapshot reports its own ingest time -- its real age. Evidence is
        # truncated, so parse leniently rather than json.loads.
        cm = re.search(r'"cypher"\s*:\s*"((?:[^"\\]|\\.)*)"', evidence)
        if cm:
            try:
                out["reproduce"] = {"cypher": json.loads(f'"{cm.group(1)}"')}
            except ValueError:
                out["reproduce"] = {"cypher": cm.group(1)}
        im = re.search(r'"ingest_time"\s*:\s*"([^"]+)"', evidence)
        if im:
            out["snapshot_ingest_time"] = im.group(1)
        if NEO4J_UI and intent == "cartography":
            out["links"] = {"neo4j": NEO4J_UI}
        return out

    reproduce = {}
    if tool == "query_prometheus" and args.get("query"):
        reproduce["promql"] = args["query"]
    cli = _kubectl(tool, args)
    if cli:
        reproduce["cli"] = cli
    if reproduce:
        out["reproduce"] = reproduce

    links = {}
    if tool == "query_prometheus" and out.get("ok") and intent == "prometheus":
        grafana = _grafana_explore(args.get("query", ""))
        if grafana:
            links["grafana"] = grafana
    ns = args.get("namespace")
    if intent == "kube" and isinstance(ns, str) and ns:
        hubble = _hubble_link(ns)
        if hubble:
            links["hubble"] = hubble
    if intent == "sandbox":
        sandbox_id = _sandbox_id(args, evidence)
        link = _sandbox_link(sandbox_id) if sandbox_id else None
        if link:
            links["sandbox"] = link
    if links:
        out["links"] = links

    return out


def enrich_all(facts, intent: str = "general") -> list:
    return [enrich(f, intent) for f in (facts or [])]
