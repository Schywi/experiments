"""Regression tests for links and tool contracts.

    python3 apps/assistant/test_guardrails.py
"""

import importlib
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import provenance        # noqa: E402
import tool_schema       # noqa: E402

_ENV_KEYS = ("GRAFANA_URL", "HUBBLE_URL", "NEO4J_UI_URL")


def _prov(**env):
    for key in _ENV_KEYS:
        os.environ.pop(key, None)
    os.environ.update(env)
    return importlib.reload(provenance)


def _prom(ok=True, query="up"):
    return {"tool": "query_prometheus", "args": {"query": query}, "ok": ok}


# --- one link kind per intent ----------------------------------------------

def test_prometheus_intent_ok_gets_grafana():
    p = _prov(GRAFANA_URL="http://g")
    f = p.enrich(_prom(), "prometheus")
    assert list((f.get("links") or {})) == ["grafana"], f.get("links")


def test_failed_query_gets_no_grafana():
    p = _prov(GRAFANA_URL="http://g")
    f = p.enrich(_prom(ok=False), "prometheus")
    assert not f.get("links"), f.get("links")


def test_kube_intent_with_namespace_gets_hubble():
    p = _prov(HUBBLE_URL="http://h")
    f = p.enrich({"tool": "get_pods", "args": {"namespace": "models"}, "ok": True}, "kube")
    assert list((f.get("links") or {})) == ["hubble"], f.get("links")
    assert f["links"]["hubble"] == "http://h/?namespace=models"


def test_kube_intent_without_namespace_gets_nothing():
    p = _prov(HUBBLE_URL="http://h")
    f = p.enrich({"tool": "get_pods", "args": {}, "ok": True}, "kube")
    assert not f.get("links"), f.get("links")


def test_wrong_intent_gets_no_link():
    p = _prov(GRAFANA_URL="http://g", HUBBLE_URL="http://h")
    prom = p.enrich(_prom(), "kube")                       # metrics fact, kube intent
    assert not prom.get("links"), prom.get("links")
    pods = p.enrich({"tool": "get_pods", "args": {"namespace": "a"}, "ok": True}, "prometheus")
    assert not pods.get("links"), pods.get("links")


def test_general_intent_gets_nothing():
    p = _prov(GRAFANA_URL="http://g", HUBBLE_URL="http://h")
    for fact in (_prom(), {"tool": "get_pods", "args": {"namespace": "a"}, "ok": True}):
        assert not p.enrich(fact, "general").get("links")


def test_cartography_intent_gets_neo4j_only():
    p = _prov(NEO4J_UI_URL="http://n", GRAFANA_URL="http://g")
    snap = {"tool": "get_topology_snapshot", "args": {}, "ok": True,
            "evidence": '{"cypher":"MATCH (n)","ingest_time":"t"}'}
    assert list((p.enrich(snap, "cartography").get("links") or {})) == ["neo4j"]
    assert not p.enrich(snap, "prometheus").get("links")


def test_argocd_and_kargo_never_appear():
    p = _prov(GRAFANA_URL="http://g", HUBBLE_URL="http://h", NEO4J_UI_URL="http://n")
    facts = [_prom(), {"tool": "get_pods", "args": {"namespace": "a"}, "ok": True},
             {"tool": "get_topology_snapshot", "args": {}, "ok": True, "evidence": "{}"}]
    for intent in ("prometheus", "kube", "cartography", "general"):
        for fact in facts:
            keys = set((p.enrich(fact, intent).get("links") or {}))
            assert "argocd" not in keys and "kargo" not in keys, (intent, keys)


# --- strict tool contracts --------------------------------------------------

def test_missing_required_arg_is_rejected():
    clean, err = tool_schema.check_args("search_logs", {"namespace": "a"})
    assert clean is None and "contains" in err, err


def test_unknown_tool_is_rejected():
    clean, err = tool_schema.check_args("run_shell", {"cmd": "ls"})
    assert clean is None and "unknown tool" in err, err


def test_wrong_type_is_rejected():
    clean, err = tool_schema.check_args("query_prometheus", {"query": 42})
    assert clean is None and "must be str" in err, err


def test_numeric_string_is_coerced():
    clean, err = tool_schema.check_args("get_pod_logs",
                                        {"namespace": "a", "pod": "p", "tail_lines": "50"})
    assert err is None and clean["tail_lines"] == 50, (clean, err)


def test_unknown_keys_are_dropped():
    clean, err = tool_schema.check_args("get_pods", {"namespace": "a", "field": "x"})
    assert err is None and "field" not in clean, (clean, err)


if __name__ == "__main__":
    failures = 0
    names = sorted(k for k in list(globals()) if k.startswith("test_"))
    for name in names:
        try:
            globals()[name]()
            print("ok  ", name)
        except Exception as exc:                       # noqa: BLE001
            failures += 1
            print("FAIL", name, "->", exc)
    print(f"\n{len(names) - failures} passed, {failures} failed")
    sys.exit(1 if failures else 0)
