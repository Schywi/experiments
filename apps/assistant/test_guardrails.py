"""Regression tests for the reported failures (links + tool contracts).

Runs with no dependencies and no cluster:

    python3 apps/assistant/test_guardrails.py

pytest also collects the `test_*` functions.
"""

import importlib
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import provenance        # noqa: E402
import tool_schema       # noqa: E402

_ENV_KEYS = ("GRAFANA_URL", "HUBBLE_URL", "ARGOCD_URL", "KARGO_URL", "NEO4J_UI_URL")


def _prov(**env):
    for key in _ENV_KEYS:
        os.environ.pop(key, None)
    os.environ.update(env)
    return importlib.reload(provenance)


# --- link honesty -----------------------------------------------------------

def test_failed_query_has_no_grafana_link():
    p = _prov(GRAFANA_URL="http://g")
    fact = p.enrich({"tool": "query_prometheus", "args": {"query": "bad("},
                     "ok": False, "error": "400 parse error", "evidence": "400"})
    assert "grafana" not in (fact.get("links") or {}), fact.get("links")


def test_ok_query_has_grafana_link():
    p = _prov(GRAFANA_URL="http://g")
    fact = p.enrich({"tool": "query_prometheus", "args": {"query": "up"}, "ok": True})
    assert "grafana" in fact["links"], fact.get("links")


def test_argocd_links_to_verified_root():
    p = _prov(ARGOCD_URL="http://a")
    fact = p.enrich({"tool": "get_pods", "args": {}, "ok": True, "evidence": "[]"})
    assert fact["links"]["argocd"] == "http://a", fact["links"]
    assert "/applications" not in fact["links"]["argocd"]


def test_kargo_link_never_carries_a_token():
    p = _prov(KARGO_URL="http://k")
    fact = p.enrich({"tool": "get_pods", "args": {}, "ok": True, "evidence": "[]"})
    link = fact["links"]["kargo"]
    assert link.startswith("http://k/projects"), link
    assert "token" not in link.lower(), link


def test_neo4j_link_only_on_the_cartography_snapshot():
    p = _prov(NEO4J_UI_URL="http://n")
    plain = p.enrich({"tool": "get_pods", "args": {}, "ok": True, "evidence": "[]"})
    assert "neo4j" not in (plain.get("links") or {}), plain.get("links")
    snap = p.enrich({"tool": "get_topology_snapshot", "args": {}, "ok": True,
                     "evidence": '{"cypher":"MATCH (n)","ingest_time":"t"}'})
    assert snap["links"]["neo4j"] == "http://n", snap["links"]


def test_links_omitted_when_env_missing():
    p = _prov()
    fact = p.enrich({"tool": "query_prometheus", "args": {"query": "up"},
                     "ok": True, "evidence": "[]"})
    assert not fact.get("links"), fact.get("links")


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


def test_unknown_keys_are_dropped_not_executed():
    clean, err = tool_schema.check_args("get_pods", {"namespace": "a", "field": "x"})
    assert err is None and "field" not in clean, (clean, err)


if __name__ == "__main__":
    failures = 0
    for name in sorted(k for k in list(globals()) if k.startswith("test_")):
        try:
            globals()[name]()
            print("ok  ", name)
        except Exception as exc:                       # noqa: BLE001
            failures += 1
            print("FAIL", name, "->", exc)
    print(f"\n{len([k for k in globals() if k.startswith('test_')]) - failures} passed, {failures} failed")
    sys.exit(1 if failures else 0)
