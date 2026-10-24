"""Unit tests for the M5 observability tools (mocked, no cluster needed).

Run where httpx + pytest are available:
    cd apps/assistant && python -m pytest -q

`tools.py` (the M1-M4 module) is stubbed when absent so this file also runs in a
checkout that has only the M5 module.
"""

import sys
import types

if "tools" not in sys.modules:  # allow running without the M1-M4 module
    _stub = types.ModuleType("tools")
    _stub.audit = lambda *a, **k: None
    _stub._core = lambda: None
    sys.modules["tools"] = _stub

import observability as obs  # noqa: E402


def test_query_prometheus_trims_and_shapes(monkeypatch):
    class FakeResp:
        def raise_for_status(self):
            pass

        def json(self):
            return {"data": {"result": [
                {"metric": {"reason": "POLICY_DENIED"}, "value": [1, "3"]},
            ]}}

    class FakeClient:
        def __init__(self, *a, **k):
            pass

        def __enter__(self):
            return self

        def __exit__(self, *a):
            return False

        def get(self, url, params):
            assert url.endswith("/api/v1/query")
            assert params["query"].startswith("sum(rate(")
            return FakeResp()

    monkeypatch.setattr(obs.httpx, "Client", FakeClient)
    monkeypatch.setattr(obs, "audit", lambda *a, **k: None)

    out = obs.query_prometheus("sum(rate(hubble_drop_total[5m])) by (reason)")
    assert out["count"] == 1
    assert out["series"][0]["metric"] == {"reason": "POLICY_DENIED"}
    assert out["series"][0]["value"] == "3"


def test_query_prometheus_truncates(monkeypatch):
    class FakeResp:
        def raise_for_status(self):
            pass

        def json(self):
            return {"data": {"result": [
                {"metric": {"i": str(i)}, "value": [1, str(i)]} for i in range(5)
            ]}}

    class FakeClient:
        def __init__(self, *a, **k):
            pass

        def __enter__(self):
            return self

        def __exit__(self, *a):
            return False

        def get(self, url, params):
            return FakeResp()

    monkeypatch.setattr(obs.httpx, "Client", FakeClient)
    monkeypatch.setattr(obs, "audit", lambda *a, **k: None)
    monkeypatch.setattr(obs, "VM_MAX_SERIES", 2)

    out = obs.query_prometheus("up")
    assert out["count"] == 5
    assert out["truncated"] is True
    assert len(out["series"]) == 2


def test_search_logs_filters_and_skips_errors(monkeypatch):
    class Pod:
        def __init__(self, name):
            self.metadata = types.SimpleNamespace(name=name)

    class FakeCore:
        def list_namespaced_pod(self, namespace):
            return types.SimpleNamespace(items=[Pod("a"), Pod("b")])

        def read_namespaced_pod_log(self, name, namespace, tail_lines):
            if name == "a":
                raise RuntimeError("no logs")
            return "all good\nERROR: boom\nstill good"

    monkeypatch.setattr(obs, "_core", lambda: FakeCore())
    monkeypatch.setattr(obs, "audit", lambda *a, **k: None)

    out = obs.search_logs("observability", "error")
    assert out["count"] == 1
    assert out["hits"][0]["pod"] == "b"
    assert "ERROR: boom" in out["hits"][0]["line"]
