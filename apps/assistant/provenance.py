"""Evidence provenance for the UI — deliberately kept OUT of the LLM context.

Each pipeline fact is `{tool, args, ok, evidence, ms}`. This module enriches a
fact with what a human needs to *reproduce* and *open* what the agent saw:

    at         UTC timestamp
    source     which system answered (kubernetes / victoriametrics / ...)
    reproduce  a copyable command, PromQL or Cypher
    links      resolvable deep links

Link kinds and their env (an unset env means the link is omitted, never guessed):

    grafana   GRAFANA_URL   Explore deep link for a query_prometheus fact
    hubble    HUBBLE_URL    "Open in Cilium": the Hubble UI is Cilium's UI
    argocd    ARGOCD_URL    Applications overview, or an Application when named
    kargo     KARGO_URL     Projects overview, or a Stage when named
    neo4j     NEO4J_UI_URL  Neo4j Browser, only for the Cartography snapshot

Hosts are passed by env as bare LoadBalancer IPs (Grafana 192.168.0.245,
Hubble 192.168.0.241, Argo CD 192.168.0.247, Kargo 192.168.0.249). No host is
written into this file. Nothing here reaches plan/correlate, so it costs the
model zero tokens.
"""

import json
import os
import re
import shlex
import urllib.parse
from datetime import datetime, timezone

GRAFANA_URL = os.environ.get("GRAFANA_URL", "").rstrip("/")
HUBBLE_URL = os.environ.get("HUBBLE_URL", "").rstrip("/")
ARGOCD_URL = os.environ.get("ARGOCD_URL", "").rstrip("/")
KARGO_URL = os.environ.get("KARGO_URL", "").rstrip("/")
GRAFANA_DATASOURCE_UID = os.environ.get("GRAFANA_DATASOURCE_UID", "")
GRAFANA_DATASOURCE_NAME = os.environ.get("GRAFANA_DATASOURCE_NAME", "victoriametrics")
# No default: an unset NEO4J_UI_URL means no neo4j link. There is no fallback host.
NEO4J_UI = os.environ.get("NEO4J_UI_URL", "").rstrip("/")

ARGOCD_NAMESPACE = os.environ.get("ARGOCD_APPLICATIONS_NAMESPACE", "argocd")

_NAMESPACE_RE = re.compile(r'"namespace"\s*:\s*"([^"]+)"')

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


def _namespace_for(args: dict, evidence: str):
    """Namespace from the tool args, else the single namespace in the evidence.

    Keeps the Cilium/Hubble link on more facts without inventing a namespace: if
    the evidence names several namespaces, it is ambiguous and no link is made.
    """
    ns = args.get("namespace")
    if isinstance(ns, str) and ns:
        return ns
    found = set(_NAMESPACE_RE.findall(evidence or ""))
    return found.pop() if len(found) == 1 else None


def _hubble_link(namespace):
    """Hubble is Cilium's UI; it is namespace-scoped only (no flow-query route)."""
    if not (HUBBLE_URL and namespace):
        return None
    return f"{HUBBLE_URL}/?namespace={urllib.parse.quote(str(namespace))}"


def _argocd_link(args: dict):
    """Applications overview, or one Application when a fact names it."""
    if not ARGOCD_URL:
        return None
    name = args.get("application") or args.get("app")
    if isinstance(name, str) and name:
        return (f"{ARGOCD_URL}/applications/"
                f"{urllib.parse.quote(str(args.get('namespace') or ARGOCD_NAMESPACE))}/"
                f"{urllib.parse.quote(name)}")
    return f"{ARGOCD_URL}/applications"


def _kargo_link(args: dict):
    """Projects overview, or a Stage when a fact names project and stage."""
    if not KARGO_URL:
        return None
    project = args.get("kargo_project") or args.get("project")
    stage = args.get("stage")
    if isinstance(project, str) and project:
        if isinstance(stage, str) and stage:
            return (f"{KARGO_URL}/projects/{urllib.parse.quote(project)}"
                    f"/stages/{urllib.parse.quote(stage)}")
        return f"{KARGO_URL}/projects/{urllib.parse.quote(project)}"
    return f"{KARGO_URL}/projects"


def enrich(fact: dict) -> dict:
    out = dict(fact or {})
    tool = out.get("tool")
    args = out.get("args") or {}
    if not isinstance(args, dict):
        args = {}
    evidence = out.get("evidence") or ""

    out["at"] = datetime.now(timezone.utc).isoformat(timespec="seconds")
    out["source"] = _SOURCE.get(tool, "unknown")

    # The Cartography snapshot reports its own ingest time -- its real age.
    if tool == "get_topology_snapshot":
        # The pipeline truncates each fact's evidence, so parse leniently with a
        # regex rather than json.loads (which fails on a truncated object).
        cm = re.search(r'"cypher"\s*:\s*"((?:[^"\\]|\\.)*)"', evidence)
        if cm:
            try:
                out["reproduce"] = {"cypher": json.loads(f'"{cm.group(1)}"')}
            except ValueError:
                out["reproduce"] = {"cypher": cm.group(1)}
        im = re.search(r'"ingest_time"\s*:\s*"([^"]+)"', evidence)
        if im:
            out["snapshot_ingest_time"] = im.group(1)
        links = {}
        if NEO4J_UI:
            links["neo4j"] = NEO4J_UI
        argocd = _argocd_link(args)
        if argocd:
            links["argocd"] = argocd
        kargo = _kargo_link(args)
        if kargo:
            links["kargo"] = kargo
        if links:
            out["links"] = links
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
    if tool == "query_prometheus":
        grafana = _grafana_explore(args.get("query", ""))
        if grafana:
            links["grafana"] = grafana
    hubble = _hubble_link(_namespace_for(args, evidence))
    if hubble:
        links["hubble"] = hubble
    argocd = _argocd_link(args)
    if argocd:
        links["argocd"] = argocd
    kargo = _kargo_link(args)
    if kargo:
        links["kargo"] = kargo
    if links:
        out["links"] = links

    return out


def enrich_all(facts) -> list:
    return [enrich(f) for f in (facts or [])]
