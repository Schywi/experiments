"""Recipe tests: the ordered chain, with the tools faked out.

    python3 apps/assistant/test_recipes.py
"""

import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import provenance  # noqa: E402
import recipes      # noqa: E402

provenance.GRAFANA_URL = "http://192.168.0.245"
provenance.HUBBLE_URL = "http://192.168.0.241"


def _recorder(overrides=None):
    calls = []
    overrides = overrides or {}

    def call(tool, args, intent_name="general"):
        calls.append((tool, args, intent_name))
        if tool in overrides:
            return overrides[tool]
        if tool == "sandbox_create":
            return {"tool": tool, "args": args, "ok": True, "intent": intent_name,
                    "evidence": json.dumps({"sandbox_id": "browser-pool-72vfg"})}
        return {"tool": tool, "args": args, "ok": True, "intent": intent_name,
                "evidence": "{}"}
    return call, calls


def test_observe_url_chains_metrics_then_sandbox():
    call, calls = _recorder()
    facts, stopped = recipes.run("observe_url", "check drops and screenshot",
                                 {}, call, lambda q: "sum(rate(hubble_drop_total[5m]))")
    assert stopped is None, stopped
    assert [c[0] for c in calls] == ["query_prometheus", "sandbox_create",
                                     "sandbox_open", "sandbox_shot"], calls
    opened = [c for c in calls if c[0] == "sandbox_open"][0]
    assert opened[1]["url"].startswith(provenance.GRAFANA_URL), opened
    assert opened[1]["sandbox_id"] == "browser-pool-72vfg", opened
    assert [f["tool"] for f in facts] == ["query_prometheus", "sandbox_create",
                                          "sandbox_open", "sandbox_shot"]


def test_observe_url_skips_metrics_when_a_url_is_given():
    call, calls = _recorder()
    facts, stopped = recipes.run("observe_url", "sandbox on google.com and screenshot",
                                 {"url": "google.com"}, call, lambda q: "ignored")
    assert stopped is None, stopped
    assert [c[0] for c in calls] == ["sandbox_create", "sandbox_open", "sandbox_shot"]
    assert [c for c in calls if c[0] == "sandbox_open"][0][1]["url"] == "google.com"


def test_failed_promql_stops_the_chain_and_creates_nothing():
    failed = {"tool": "query_prometheus", "args": {}, "ok": False,
              "error": "bad query", "evidence": "bad query"}
    call, calls = _recorder({"query_prometheus": failed})
    facts, stopped = recipes.run("observe_url", "check drops", {}, call, lambda q: "bogus(")
    assert stopped == "prometheus-failed", stopped
    assert [c[0] for c in calls] == ["query_prometheus"], calls
    assert len(facts) == 1 and facts[0]["ok"] is False


def test_create_without_an_id_stops_before_open():
    created = {"tool": "sandbox_create", "args": {}, "ok": True, "evidence": "{}"}
    call, calls = _recorder({"sandbox_create": created})
    facts, stopped = recipes.run("observe_url", "x", {"url": "google.com"},
                                 call, lambda q: "")
    assert stopped == "create-failed", stopped
    assert [c[0] for c in calls] == ["sandbox_create"], calls


def test_observe_cilium_uses_the_namespace_url():
    call, calls = _recorder()
    facts, stopped = recipes.run("observe_cilium", "what is happening in worm-lab",
                                 {"namespace": "worm-lab"}, call, None)
    assert stopped is None, stopped
    assert calls[0][0] == "get_pods" and calls[0][1] == {"namespace": "worm-lab"}
    opened = [c for c in calls if c[0] == "sandbox_open"][0]
    assert opened[1]["url"] == "http://192.168.0.241/?namespace=worm-lab", opened


def test_observe_cilium_needs_a_namespace():
    call, calls = _recorder()
    facts, stopped = recipes.run("observe_cilium", "x", {}, call, None)
    assert stopped == "no-namespace" and calls == []


def test_unknown_recipe_reports_itself():
    call, calls = _recorder()
    facts, stopped = recipes.run("nope", "x", {}, call, None)
    assert stopped == "unknown-recipe:nope" and facts == []


def test_is_chain_upgrades_a_metrics_to_sandbox_list():
    assert recipes.is_chain(["query_prometheus", "sandbox_create", "sandbox_shot"])
    assert recipes.is_chain(["sandbox_open", "query_prometheus"])
    assert not recipes.is_chain(["query_prometheus", "get_pods"])
    assert not recipes.is_chain(["sandbox_create", "sandbox_shot"])
    assert not recipes.is_chain(["sandbox_shot"])


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
