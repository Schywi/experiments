"""Synthetic sessions: creating browser contexts and reading them back.

The fake CDP records every command, so these tests assert the exact protocol
traffic the console sends — which matters because `Target.createBrowserContext`
on a page socket is refused with "Not allowed", and the bridge's browser-socket
routing is what makes these calls legal.
"""

import asyncio

import pytest
from fastapi.testclient import TestClient

import config
import fixtures
import inventory
import kube
import render
import server
import traffic


class FakeCDP:
    """Stands in for cdp.RouterCDP: records commands, models Chromium's answers."""

    def __init__(self, refuse=None, pages=None):
        self.calls: list[tuple[str, str, dict]] = []
        self.contexts: list[str] = []
        self._refuse = refuse or {}
        self._pages = pages or {}

    async def call(self, sandbox_id, method, params=None):
        self.calls.append((sandbox_id, method, params or {}))
        if method in self._refuse:
            raise RuntimeError(self._refuse[method])
        if method == "Target.createBrowserContext":
            context_id = f"ctx-{len(self.contexts) + 1:02d}"
            self.contexts.append(context_id)
            return {"browserContextId": context_id}
        if method == "Target.createTarget":
            return {"targetId": "target-" + params["browserContextId"]}
        if method == "Target.getBrowserContexts":
            return {"browserContextIds": list(self.contexts)}
        if method == "Target.getTargets":
            return {"targetInfos": list(self._pages.values())}
        if method == "Target.disposeBrowserContext":
            self.contexts.remove(params["browserContextId"])
            return {}
        raise AssertionError(f"unexpected command {method}")

    def methods(self):
        return [method for _, method, _ in self.calls]

    async def close(self, sandbox_id):  # the lifespan closes sockets on shutdown
        return None

    async def close_all(self):
        return None


@pytest.fixture(autouse=True)
def clean_ledger():
    traffic.LEDGER.by_sandbox.clear()
    yield
    traffic.LEDGER.by_sandbox.clear()


# --------------------------------------------------------------------------- #
# The module
# --------------------------------------------------------------------------- #
def test_start_creates_one_context_per_session_each_on_its_own_page():
    cdp = FakeCDP()
    created = asyncio.run(traffic.start(cdp, "browser-7k2", 3))
    assert len(created) == 3
    assert cdp.methods() == [
        "Target.createBrowserContext",
        "Target.createTarget",
    ] * 3
    targets = [params for _, method, params in cdp.calls if method == "Target.createTarget"]
    assert [target["url"] for target in targets] == list(traffic.DEFAULT_SITES[:3])
    assert [target["browserContextId"] for target in targets] == ["ctx-01", "ctx-02", "ctx-03"]
    # Distinct sites, which is the point: same-origin sessions share caches.
    assert len({target["url"] for target in targets}) == 3
    assert set(traffic.LEDGER.ours("browser-7k2")) == {"ctx-01", "ctx-02", "ctx-03"}


def test_start_honours_the_site_list_and_cycles_it():
    cdp = FakeCDP()
    created = asyncio.run(
        traffic.start(cdp, "browser-7k2", 3, urls=["https://a.example/", "https://b.example/"])
    )
    assert [session.url for session in created] == [
        "https://a.example/",
        "https://b.example/",
        "https://a.example/",
    ]


def test_start_is_capped_by_the_configured_density(monkeypatch):
    monkeypatch.setattr(config, "MAX_SESSIONS_PER_BROWSER", 2)
    cdp = FakeCDP()
    assert len(asyncio.run(traffic.start(cdp, "browser-7k2", 50))) == 2


def test_start_reports_the_browser_refusal_it_actually_gets():
    # The exact failure a page-socket bridge produces, kept as a regression.
    cdp = FakeCDP(refuse={"Target.createBrowserContext": "Not allowed"})
    with pytest.raises(traffic.TrafficError) as caught:
        asyncio.run(traffic.start(cdp, "browser-7k2", 1))
    assert "Not allowed" in str(caught.value)
    assert traffic.LEDGER.ours("browser-7k2") == {}


def test_listing_pairs_each_context_with_the_page_inside_it():
    cdp = FakeCDP()
    asyncio.run(traffic.start(cdp, "browser-7k2", 2))
    cdp._pages = {
        "ctx-01": {"type": "page", "browserContextId": "ctx-01", "targetId": "t1",
                   "url": "https://example.com/"},
        "ctx-02": {"type": "page", "browserContextId": "ctx-02", "targetId": "t2",
                   "url": "https://example.org/"},
        # A page in the default context: listed by getTargets, absent from
        # getBrowserContexts, so it never appears as a session.
        "default": {"type": "page", "browserContextId": "default-ctx", "targetId": "t0",
                    "url": "about:blank"},
    }
    rows = asyncio.run(traffic.listing(cdp, "browser-7k2"))
    assert [row["browser_context_id"] for row in rows] == ["ctx-01", "ctx-02"]
    assert [row["urls"] for row in rows] == [["https://example.com/"], ["https://example.org/"]]
    assert all(row["created_by_console"] for row in rows)


def test_listing_marks_a_context_the_runner_created():
    cdp = FakeCDP()
    cdp.contexts = ["runner-ctx"]
    cdp._pages = {
        "runner": {"type": "page", "browserContextId": "runner-ctx", "targetId": "t9",
                   "url": "https://httpbin.org/html"}
    }
    rows = asyncio.run(traffic.listing(cdp, "browser-7k2"))
    assert rows[0]["created_by_console"] is False
    assert rows[0]["age_s"] is None


def test_stop_disposes_only_what_the_console_created():
    cdp = FakeCDP()
    asyncio.run(traffic.start(cdp, "browser-7k2", 2))
    # A context session-lab owns must survive a console stop.
    cdp.contexts.append("runner-ctx")
    result = asyncio.run(traffic.stop(cdp, "browser-7k2"))
    assert result["disposed"] == ["ctx-01", "ctx-02"]
    assert cdp.contexts == ["runner-ctx"]
    assert traffic.LEDGER.ours("browser-7k2") == {}


def test_stop_with_all_disposes_every_non_default_context():
    cdp = FakeCDP()
    asyncio.run(traffic.start(cdp, "browser-7k2", 1))
    cdp.contexts.append("runner-ctx")
    result = asyncio.run(traffic.stop(cdp, "browser-7k2", all_contexts=True))
    assert sorted(result["disposed"]) == ["ctx-01", "runner-ctx"]
    assert cdp.contexts == []


# --------------------------------------------------------------------------- #
# The routes
# --------------------------------------------------------------------------- #
def fake_snapshot(namespace: str) -> kube.Snapshot:
    return kube.Snapshot(
        sandboxes=fixtures.copy_of(fixtures.READY_SANDBOX),
        claims=fixtures.copy_of(fixtures.CLAIM),
        pods=fixtures.copy_of(*fixtures.PODS),
        usage=dict(fixtures.USAGE),
        errors=[],
    )


@pytest.fixture
def fake(monkeypatch):
    stub = FakeCDP()
    monkeypatch.setattr(server.SCREEN, "cdp", stub)
    return stub


@pytest.fixture
def client(monkeypatch, fake):
    monkeypatch.setattr(kube, "snapshot", fake_snapshot)
    monkeypatch.setattr(server, "kube", kube)
    with TestClient(server.app) as test_client:
        yield test_client


SANDBOX = "sb-4f2a"


def test_post_contexts_starts_traffic_and_the_ledger_remembers(client):
    response = client.post(f"/sandboxes/{SANDBOX}/contexts", json={"count": 2})
    assert response.status_code == 202
    payload = response.json()
    assert payload["sandbox"] == SANDBOX
    assert len(payload["created"]) == 2
    assert payload["created"][0]["url"] == traffic.DEFAULT_SITES[0]
    assert payload["limit"] == config.MAX_SESSIONS_PER_BROWSER


def test_post_contexts_accepts_its_own_site_list(client):
    response = client.post(
        f"/sandboxes/{SANDBOX}/contexts",
        json={"count": 2, "urls": ["https://one.example/", "https://two.example/"]},
    )
    assert [item["url"] for item in response.json()["created"]] == [
        "https://one.example/",
        "https://two.example/",
    ]


def test_post_contexts_is_refused_when_writes_are_off(client, monkeypatch):
    monkeypatch.setattr(config, "WRITE_ENABLED", False)
    assert client.post(f"/sandboxes/{SANDBOX}/contexts", json={"count": 1}).status_code == 403


def test_get_contexts_reports_the_browser_refusal_as_502(client, monkeypatch):
    monkeypatch.setattr(
        server.SCREEN, "cdp", FakeCDP(refuse={"Target.getBrowserContexts": "Not allowed"})
    )
    response = client.get(f"/sandboxes/{SANDBOX}/contexts")
    assert response.status_code == 502
    assert "Not allowed" in response.json()["detail"]


def test_get_contexts_after_starting_traffic(client, fake):
    client.post(f"/sandboxes/{SANDBOX}/contexts", json={"count": 2})
    fake._pages = {
        "ctx-01": {"type": "page", "browserContextId": "ctx-01", "targetId": "t1",
                   "url": "https://example.com/"},
        "ctx-02": {"type": "page", "browserContextId": "ctx-02", "targetId": "t2",
                   "url": "https://example.org/"},
    }
    payload = client.get(f"/sandboxes/{SANDBOX}/contexts").json()
    assert len(payload["contexts"]) == 2
    assert payload["contexts"][0]["urls"] == ["https://example.com/"]
    assert payload["sites"] == list(traffic.DEFAULT_SITES)


def test_delete_contexts_disposes_them(client):
    client.post(f"/sandboxes/{SANDBOX}/contexts", json={"count": 2})
    response = client.delete(f"/sandboxes/{SANDBOX}/contexts")
    assert response.status_code == 200
    assert len(response.json()["disposed"]) == 2
    assert client.get(f"/sandboxes/{SANDBOX}/contexts").json()["contexts"] == []


def test_contexts_of_an_unknown_sandbox_is_404(client):
    assert client.get("/sandboxes/nope/contexts").status_code == 404


# --------------------------------------------------------------------------- #
# The control
# --------------------------------------------------------------------------- #
def test_the_dialog_carries_a_traffic_form_with_its_limit():
    report = inventory.build_report(
        fixtures.copy_of(fixtures.READY_SANDBOX),
        claims=fixtures.copy_of(fixtures.CLAIM),
        pods=fixtures.copy_of(*fixtures.PODS),
        usage=dict(fixtures.USAGE),
    )
    markup = render.expanded(report.tiles[0])
    assert 'data-act="traffic-start"' in markup
    assert 'data-act="traffic-stop"' in markup
    assert f'max="{config.MAX_SESSIONS_PER_BROWSER}"' in markup
    assert 'data-contexts="sb-4f2a"' in markup


def test_the_page_ships_the_traffic_script():
    report = inventory.build_report(
        fixtures.copy_of(fixtures.READY_SANDBOX),
        claims=fixtures.copy_of(fixtures.CLAIM),
        pods=fixtures.copy_of(*fixtures.PODS),
        usage=dict(fixtures.USAGE),
    )
    page = render.page(report)
    assert "/contexts" in page
    assert "start traffic" in page
    assert "__TRAFFIC_JS__" not in page
