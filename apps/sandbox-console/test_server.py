"""HTTP surface tests: the seven routes, and the ones that must not exist.

`kube.snapshot` and the CDP socket are replaced with fakes, so this runs with no
cluster and no browser.
"""

import json

import pytest
from fastapi.testclient import TestClient

import fixtures
import inventory
import kube
import probes
import server


def fake_snapshot(namespace: str) -> kube.Snapshot:
    return kube.Snapshot(
        sandboxes=fixtures.copy_of(fixtures.READY_SANDBOX, fixtures.POOL_MEMBER),
        claims=fixtures.copy_of(fixtures.CLAIM),
        pods=fixtures.copy_of(*fixtures.PODS),
        usage=dict(fixtures.USAGE),
        errors=[],
    )


@pytest.fixture
def client(monkeypatch):
    monkeypatch.setattr(kube, "snapshot", fake_snapshot)
    monkeypatch.setattr(server, "kube", kube)
    with TestClient(server.app) as test_client:
        yield test_client


def test_index_renders_the_tiles(client):
    response = client.get("/")
    assert response.status_code == 200
    assert "sandbox-console" in response.text
    assert "sb-4f2a" in response.text
    # The unclaimed pool member is counted, not drawn.
    assert "browser-pool-7mqd9" not in response.text


def test_grid_fragment_is_html(client):
    response = client.get("/sandboxes")
    assert response.status_code == 200
    assert response.headers["content-type"].startswith("text/html")
    assert 'class="card"' in response.text


def test_shot_returns_a_png_and_a_cache_header(client, monkeypatch):
    async def fake_shot(sandbox_id, fresh=False):
        return b"\x89PNG\r\n"

    monkeypatch.setattr(server.SCREEN, "shot", fake_shot)
    response = client.get("/sandboxes/sb-4f2a/shot")
    assert response.status_code == 200
    assert response.headers["content-type"] == "image/png"
    assert "max-age=" in response.headers["cache-control"]
    assert response.content == b"\x89PNG\r\n"


def test_shot_reports_a_router_failure_instead_of_hanging(client, monkeypatch):
    import cdp

    async def failing_shot(sandbox_id, fresh=False):
        raise cdp.RouterError(502, "Could not connect to the backend sandbox: sb-4f2a")

    monkeypatch.setattr(server.SCREEN, "shot", failing_shot)
    response = client.get("/sandboxes/sb-4f2a/shot")
    assert response.status_code == 502
    assert "Could not connect" in response.text


def test_click_returns_the_refreshed_tile(client, monkeypatch):
    calls = {}

    async def fake_click(sandbox_id, payload):
        calls["sandbox"] = sandbox_id
        calls["payload"] = payload

    monkeypatch.setattr(server.SCREEN, "click", fake_click)
    response = client.post("/sandboxes/sb-4f2a/click", json={"x": 100, "y": 50, "scale": 2.0})
    assert response.status_code == 200
    assert 'id="tile-sb-4f2a"' in response.text
    assert calls["sandbox"] == "sb-4f2a"
    assert calls["payload"].scale == 2.0


def test_click_on_an_unknown_sandbox_is_404(client):
    response = client.post("/sandboxes/nope/click", json={"x": 1, "y": 1})
    assert response.status_code == 404


def test_probe_returns_the_tile_with_the_badge(client, monkeypatch):
    async def fake_probe(sandbox_id, tile):
        return {"cluster": "blocked"}

    async def fake_pod(namespace, name):
        return {"spec": {"dnsPolicy": "None", "automountServiceAccountToken": False}}

    monkeypatch.setattr(server.SCREEN, "probe", fake_probe)
    monkeypatch.setattr(server.kube, "pod", fake_pod)
    response = client.post("/sandboxes/sb-4f2a/reachability")
    assert response.status_code == 200
    assert "isolation not probed" in response.text or "blocked" in response.text


def test_expanded_returns_the_modal(client):
    response = client.get("/sandboxes/sb-4f2a/expanded")
    assert response.status_code == 200
    assert 'class="dialog-backdrop"' in response.text


def test_healthz_echoes_config_without_secrets(client):
    response = client.get("/healthz")
    assert response.status_code == 200
    body = response.json()
    assert body["ok"] is True
    assert body["viewport"] == [1280, 600]
    assert body["tiles"] == 1
    assert "token" not in {key.lower() for key in body}


def test_the_verbs_are_exactly_get_post_and_delete():
    """M4 added create/delete; nothing exotic, and never pods/exec."""
    methods = set()
    for route in server.app.routes:
        methods.update(getattr(route, "methods", set()) or set())
    assert methods == {"GET", "POST", "DELETE", "HEAD"}
    paths = {getattr(route, "path", "") for route in server.app.routes}
    assert not any("pods/exec" in path for path in paths)
    assert "/sandboxes" not in {path for path in paths if path.endswith("/delete")}


def test_router_token_prefers_the_environment(monkeypatch):
    monkeypatch.setenv("SANDBOX_ROUTER_TOKEN", "  env-token  ")
    assert kube.router_token() == "env-token"


def test_screen_notes_clear_after_a_successful_capture():
    screen = server.Screen()
    screen.note("sb-1", "shot failed: router 502")
    assert screen._notes["sb-1"].startswith("shot failed")
    screen.store_shot("sb-1", b"png")
    assert "sb-1" not in screen._notes


def test_reachability_cache_is_keyed_on_the_pod_ip():
    screen = server.Screen()
    payload = {"cluster": "blocked"}
    screen.store_reach("sb-1", "10.42.0.36", payload)
    assert screen.reach("sb-1", "10.42.0.36") == payload
    # A new pod means the old evidence is not evidence about this pod.
    assert screen.reach("sb-1", "10.42.0.99") is None


def test_report_decorates_tiles_from_the_local_stores(monkeypatch):
    monkeypatch.setattr(kube, "snapshot", fake_snapshot)
    screen = server.Screen()
    screen.store_shot("sb-4f2a", b"png")
    screen.store_reach("sb-4f2a", "10.42.0.36", {"cluster": "blocked"})
    report = __import__("asyncio").run(screen.report())
    tile = report.tiles[0]
    assert tile.screenshot_ts > 0
    assert tile.reachability == {"cluster": "blocked"}


def test_probe_spec_claims_come_from_the_pod(client):
    pod = fixtures.copy_of(*fixtures.PODS)[0]
    claims = probes.spec_claims(pod)
    assert claims["sa_token"] == "absent"
    assert claims["cluster_dns"].startswith("none:")


def test_inventory_report_has_no_pool_member_tile():
    snapshot = fake_snapshot("sandbox-demo")
    report = inventory.build_report(
        snapshot.sandboxes,
        claims=snapshot.claims,
        pods=snapshot.pods,
        usage=snapshot.usage,
    )
    assert [tile.id for tile in report.tiles] == ["sb-4f2a"]
    assert report.pool_idle == 1


# --------------------------------------------------------------------------- #
# M4: create and delete
# --------------------------------------------------------------------------- #
def test_create_claim_returns_202_and_audits(client, monkeypatch):
    import lifecycle

    def fake_claim(namespace, pool, name=None):
        return lifecycle.Created("claim", "claim-1", namespace, 0.0)

    monkeypatch.setattr(lifecycle, "create_claim", fake_claim)
    monkeypatch.setattr(server.lifecycle, "create_claim", fake_claim)
    response = client.post("/sandboxes", json={"kind": "claim"})
    assert response.status_code == 202
    assert response.json()["kind"] == "claim"
    assert response.json()["name"] == "claim-1"


def test_create_is_refused_when_writes_are_off(client, monkeypatch):
    monkeypatch.setattr(server.config, "WRITE_ENABLED", False)
    response = client.post("/sandboxes", json={"kind": "claim"})
    assert response.status_code == 403
    assert "write access is disabled" in response.text


def test_creating_past_the_ceiling_is_refused(client, monkeypatch):
    monkeypatch.setattr(server.config, "MAX_CONCURRENT_SANDBOXES", 1)
    response = client.post("/sandboxes", json={"kind": "claim"})
    assert response.status_code == 409
    assert "ceiling is 1" in response.text


def test_bare_sandbox_needs_allow_raw(client, monkeypatch):
    monkeypatch.setattr(server.config, "ALLOW_RAW_SANDBOX", False)
    response = client.post("/sandboxes", json={"kind": "sandbox"})
    assert response.status_code == 403
    assert "allowRawSandbox is off" in response.text


def test_delete_removes_the_owning_claim(client, monkeypatch):
    monkeypatch.setattr(server.lifecycle, "delete_any", lambda namespace, name: f"claim/{name}-claim")
    response = client.delete("/sandboxes/browser-pool-2fngr")
    assert response.status_code == 202
    assert response.json()["removed"] == "claim/browser-pool-2fngr-claim"


def test_delete_rejects_a_non_dns_name(client):
    response = client.delete("/sandboxes/Bad_Name")
    assert response.status_code == 400


# --------------------------------------------------------------------------- #
# M5: recipes
# --------------------------------------------------------------------------- #
def test_recipe_list_names_what_can_run(client):
    response = client.get("/recipes")
    assert response.status_code == 200
    names = {item["name"] for item in response.json()["recipes"]}
    assert names == {"claim-burst", "burst-then-teardown", "capacity-check"}


def test_recipe_streams_ndjson(client, monkeypatch):
    async def fake_stream(name, count=None, hold=0.0):
        yield {"line": "hello"}
        yield {"done": True}

    monkeypatch.setattr(server.recipes, "stream", fake_stream)
    response = client.post("/recipes/claim-burst?count=2")
    assert response.status_code == 200
    assert response.headers["content-type"].startswith("application/x-ndjson")
    lines = [line for line in response.text.splitlines() if line.strip()]
    assert json.loads(lines[0]) == {"line": "hello"}
    assert json.loads(lines[-1]) == {"done": True}


def test_unknown_recipe_is_404(client):
    response = client.post("/recipes/nope")
    assert response.status_code == 404


def test_a_running_recipe_refuses_a_second_request(client, monkeypatch):
    # The refusal has to happen before the stream opens: once the response is
    # committed, a 409 is impossible.
    monkeypatch.setattr(server.recipes, "busy_with", lambda: "claim-burst")
    response = client.post("/recipes/claim-burst")
    assert response.status_code == 409
    assert "already running" in response.text


# --------------------------------------------------------------------------- #
# M6 / M7: compare and the demo run
# --------------------------------------------------------------------------- #
def test_compare_page_renders_without_a_run(client, monkeypatch):
    import compare

    compare.HISTORY.clear()
    # The page now falls back to the database after a restart, so isolate the
    # empty state from whatever earlier tests wrote there.
    monkeypatch.setattr(server.compare_mode, "latest_run", lambda: None)
    response = client.get("/compare")
    assert response.status_code == 200
    assert "No comparison has run yet" in response.text


def test_compare_post_runs_both_arms(client, monkeypatch):
    import compare

    class Arm:
        label = "CLAIM (warm pool)"
        kind = "claim"

        def __init__(self):
            self.timers = {"t_ready_ms": 1553}
            self.error = None
            self.reachability = {"cluster": "blocked"}

        def to_dict(self):
            return {"label": self.label, "timers": self.timers, "error": None}

    class Run:
        def __init__(self):
            self.arms = [Arm()]

        def to_dict(self):
            return {"arms": [arm.to_dict() for arm in self.arms], "note": None}

    async def fake_run(client_cdp, allow_raw, timeout=90.0):
        return Run()

    monkeypatch.setattr(server.compare_mode, "run_compare", fake_run)
    response = client.post("/compare")
    assert response.status_code == 200
    assert response.json()["arms"][0]["timers"]["t_ready_ms"] == 1553


def test_demo_fill_claims_up_to_the_target(client, monkeypatch):
    import lifecycle

    created = []

    def fake_claim(namespace, pool, name=None):
        created.append(name)
        return lifecycle.Created("claim", f"demo-{len(created)}", namespace, 0.0)

    monkeypatch.setattr(server.lifecycle, "create_claim", fake_claim)
    monkeypatch.setattr(server.config, "DEMO_TILES", 3)
    monkeypatch.setattr(server.config, "MAX_CONCURRENT_SANDBOXES", 9)
    response = client.post("/demo")
    assert response.status_code == 202
    # one tile is already running in the fixture, so the demo adds two
    assert len(response.json()["created"]) == 2


def test_demo_teardown_deletes_only_managed_claims(client, monkeypatch):
    monkeypatch.setattr(
        server.lifecycle,
        "managed_claims",
        lambda namespace: [{"metadata": {"name": "demo-1"}}, {"metadata": {"name": "demo-2"}}],
    )
    # The bare compare column is a Sandbox with no claim, so teardown sweeps
    # those separately.
    monkeypatch.setattr(
        server.lifecycle,
        "managed_sandboxes",
        lambda namespace: [{"metadata": {"name": "bare-1"}}],
    )
    removed = []
    monkeypatch.setattr(server.lifecycle, "delete_claim", lambda namespace, name: removed.append(name))
    monkeypatch.setattr(server.lifecycle, "delete_sandbox", lambda namespace, name: removed.append(name))
    response = client.post("/demo/teardown")
    assert response.status_code == 200
    assert sorted(removed) == ["bare-1", "demo-1", "demo-2"]
    assert response.json()["removed"] == ["demo-1", "demo-2", "bare-1"]


# --------------------------------------------------------------------------- #
# vendored front-end assets
# --------------------------------------------------------------------------- #
def test_the_unocss_runtime_is_served_locally(client):
    """No CDN at runtime: the pane renders on a LAN with no egress."""
    response = client.get("/static/unocss.js")
    assert response.status_code == 200
    assert response.headers["content-type"].startswith("text/javascript")
    assert len(response.content) > 50_000
    assert "max-age" in response.headers["cache-control"]


def test_static_route_refuses_to_escape_its_directory(client):
    response = client.get("/static/..%2Fserver.py")
    assert response.status_code == 404


def test_the_page_loads_the_runtime_and_its_config(client):
    page = client.get("/").text
    assert '<script src="/static/unocss.js"></script>' in page
    assert "window.__unocss" in page
    assert "hsl(var(--primary))" in page


def test_compare_from_a_card_returns_the_two_columns(client, monkeypatch):
    class Arm:
        def __init__(self, label, kind):
            self.label = label
            self.kind = kind
            self.timers = {"t_ready_ms": 123 if kind == "recorded" else 3055}
            self.reachability = {"cluster": "blocked"}
            self.error = None
            self.sandbox = "browser-pool-2fngr" if kind == "recorded" else "bare-1"

        def total_ms(self):
            return max(self.timers.values())

    class Run:
        def __init__(self):
            self.arms = [Arm("THIS SANDBOX", "recorded"), Arm("BARE SANDBOX", "sandbox")]
            self.note = None

    async def fake_run_against(client_cdp, sandbox_id, allow_raw, timeout=90.0):
        return Run()

    monkeypatch.setattr(server.compare_mode, "run_against", fake_run_against)
    response = client.post("/sandboxes/browser-pool-2fngr/compare")
    assert response.status_code == 200
    assert "THIS SANDBOX" in response.text
    assert "BARE SANDBOX" in response.text
    assert 'data-act="back"' in response.text


def test_compare_from_a_card_is_refused_when_writes_are_off(client, monkeypatch):
    monkeypatch.setattr(server.config, "WRITE_ENABLED", False)
    assert client.post("/sandboxes/browser-pool-2fngr/compare").status_code == 403


def test_healthz_reports_whether_timings_are_durable(client):
    body = client.get("/healthz").json()
    assert "durable_timings" in body


# --------------------------------------------------------------------------- #
# the frame after a click, and the url a card offers
# --------------------------------------------------------------------------- #
def test_a_fresh_shot_skips_the_cache(client, monkeypatch):
    seen = {}

    async def fake_shot(sandbox_id, fresh=False):
        seen["fresh"] = fresh
        return b"\x89PNG\r\n"

    monkeypatch.setattr(server.SCREEN, "shot", fake_shot)
    assert client.get("/sandboxes/sb-4f2a/shot").status_code == 200
    assert seen["fresh"] is False
    assert client.get("/sandboxes/sb-4f2a/shot?fresh=1").status_code == 200
    assert seen["fresh"] is True


def test_opening_a_url_returns_the_tile_with_the_new_page(client, monkeypatch):
    seen = {}

    async def fake_navigate(sandbox_id, url):
        seen["sandbox"] = sandbox_id
        seen["url"] = url
        server.SCREEN.store_url(sandbox_id, url)
        return url

    monkeypatch.setattr(server.SCREEN, "navigate", fake_navigate)
    try:
        response = client.post("/sandboxes/sb-4f2a/url", json={"url": "https://wikipedia.org/"})
        assert response.status_code == 200
        assert seen == {"sandbox": "sb-4f2a", "url": "https://wikipedia.org/"}
        assert "https://wikipedia.org/" in response.text
        assert 'class="url-row"' in response.text
    finally:
        server.SCREEN._urls.pop("sb-4f2a", None)


def test_opening_a_url_on_an_unknown_sandbox_is_404(client):
    response = client.post("/sandboxes/nope/url", json={"url": "https://example.com/"})
    assert response.status_code == 404


@pytest.mark.parametrize(
    "raw,expected",
    [
        ("example.com", "https://example.com"),
        ("http://example.com/a", "http://example.com/a"),
        ("https://example.com/?q=1", "https://example.com/?q=1"),
        ("  https://example.com/  ", "https://example.com/"),
    ],
)
def test_a_bare_host_is_read_as_https(raw, expected):
    assert server.clean_url(raw) == expected


@pytest.mark.parametrize(
    "raw",
    [
        "",
        "   ",
        "file:///etc/passwd",
        "javascript:alert(1)",
        "data:text/html,hi",
        "ftp://example.com/",
        "chrome://settings",
        "https://",
        "https://example.com:99999/",
        "https://example.com/" + "a" * 2100,
    ],
)
def test_anything_but_http_and_https_is_refused(raw):
    from fastapi import HTTPException

    with pytest.raises(HTTPException) as failure:
        server.clean_url(raw)
    assert failure.value.status_code == 400


def test_navigation_stores_the_frame_and_the_page(monkeypatch):
    import asyncio

    screen = server.Screen()
    calls = {}

    async def fake_navigate(sandbox_id, url):
        calls["url"] = url
        return "https://wikipedia.org/"

    async def fake_screenshot(sandbox_id):
        calls["shot"] = True
        return b"\x89PNG\r\n"

    monkeypatch.setattr(screen.cdp, "navigate", fake_navigate)
    monkeypatch.setattr(screen.cdp, "screenshot", fake_screenshot)
    landed = asyncio.run(screen.navigate("sb-1", "https://wikipedia.org"))
    assert landed == "https://wikipedia.org/"
    assert screen.url("sb-1") == "https://wikipedia.org/"
    assert screen.shot_ts("sb-1") > 0
    assert calls["shot"] is True


def test_the_first_frame_teaches_the_pane_which_page_it_is(monkeypatch):
    import asyncio

    screen = server.Screen()

    async def fake_screenshot(sandbox_id):
        return b"\x89PNG\r\n"

    async def fake_location(sandbox_id):
        return "https://example.com/"

    monkeypatch.setattr(screen.cdp, "screenshot", fake_screenshot)
    monkeypatch.setattr(screen.cdp, "location", fake_location)
    asyncio.run(screen.shot("sb-1"))
    assert screen.url("sb-1") == "https://example.com/"


def test_deleting_a_sandbox_forgets_its_page(monkeypatch):
    import asyncio

    screen = server.Screen()
    screen.store_url("sb-1", "https://example.com/")
    monkeypatch.setattr(server.config, "WRITE_ENABLED", True)
    monkeypatch.setattr(server.lifecycle, "delete_any", lambda namespace, name: f"claim/{name}")
    asyncio.run(screen.delete("sb-1"))
    assert screen.url("sb-1") is None
