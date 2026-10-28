"""Recipes: one ordered chain with a data handoff.

Deterministic Python. The only LLM use is the PromQL writer the caller passes in.
A step that fails keeps its fact and stops the chain; `stopped` names the step.
See research/features/assistant/recipes.md.
"""

import json

import provenance


def _sandbox_id(fact: dict) -> str:
    try:
        payload = json.loads(fact.get("evidence") or "{}")
    except (ValueError, TypeError):
        return ""
    if not isinstance(payload, dict):
        return ""
    return str(payload.get("sandbox_id") or "")


def _create_and_frame(facts, call, url):
    """create -> open(url) -> shot, appending each fact."""
    created = call("sandbox_create", {}, "sandbox")
    facts.append(created)
    sandbox_id = _sandbox_id(created)
    if not sandbox_id:
        return facts, "create-failed"
    facts.append(call("sandbox_open", {"sandbox_id": sandbox_id, "url": url}, "sandbox"))
    facts.append(call("sandbox_shot", {"sandbox_id": sandbox_id}, "sandbox"))
    return facts, None


def observe_url(question, args, call, write_promql):
    """metrics (optional) -> grafana url -> sandbox -> open -> shot."""
    facts = []
    url = str((args or {}).get("url") or "").strip()
    if not url:
        query = write_promql(question)
        if not query:
            return facts, "no-promql"
        fact = call("query_prometheus", {"query": query}, "prometheus")
        facts.append(fact)
        if not fact.get("ok"):
            return facts, "prometheus-failed"
        url = provenance.grafana_url(query) or ""
        if not url:
            return facts, "no-grafana-url"
    return _create_and_frame(facts, call, url)


def observe_cilium(question, args, call, write_promql):
    """namespace evidence -> hubble url -> sandbox -> open -> shot."""
    facts = []
    namespace = str((args or {}).get("namespace") or "").strip()
    if not namespace:
        return facts, "no-namespace"
    facts.append(call("get_pods", {"namespace": namespace}, "kube"))
    url = provenance.hubble_url(namespace) or ""
    if not url:
        return facts, "no-hubble-url"
    return _create_and_frame(facts, call, url)


def is_chain(tool_names) -> bool:
    """True when a flat task list is really one ordered chain.

    metrics + (create or open) + (shot or open) means the frame needs a URL the
    metrics step produces, and the sandbox needs an id the create produces. Running
    those as parallel tasks drops both handoffs, so the caller runs observe_url
    instead and ignores the arguments the model guessed.
    """
    names = set(tool_names)
    return ("query_prometheus" in names
            and bool(names & {"sandbox_create", "sandbox_open"})
            and bool(names & {"sandbox_shot", "sandbox_open"}))


def run(name, question, args, call, write_promql=None):
    """Return (facts, stopped). `call(tool, args, intent) -> fact`."""
    if name == "observe_url":
        return observe_url(question, args, call, write_promql)
    if name == "observe_cilium":
        return observe_cilium(question, args, call, write_promql)
    return [], f"unknown-recipe:{name}"
