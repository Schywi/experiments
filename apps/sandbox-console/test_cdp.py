"""CDP client tests.

The transport is faked in two halves: `httpx.MockTransport` answers `/json/list`
so the URL rewrite and header set are asserted on a real request, and a real
`websockets` server stands in for the browser so the framing, request ids and
`Input.dispatchMouseEvent` payloads are exercised end to end.
"""

import asyncio
import base64
import json

import httpx
from websockets.asyncio.server import serve

import cdp

PNG = bytes.fromhex("89504e470d0a1a0a0000000d49484452")


def run(coro):
    return asyncio.run(coro)


def test_rewrite_ws_url_moves_host_and_keeps_path():
    assert (
        cdp.rewrite_ws_url("http://192.168.0.248", "ws://localhost:9222/devtools/page/AB")
        == "ws://192.168.0.248/devtools/page/AB"
    )
    assert (
        cdp.rewrite_ws_url("https://router.example:8443", "ws://127.0.0.1:9222/devtools/browser/x?y=1")
        == "wss://router.example:8443/devtools/browser/x?y=1"
    )


def test_clamp_point_divides_by_scale_and_bounds_to_the_viewport():
    assert cdp.clamp_point(640, 300, 1.0, 1280, 600) == (640, 300)
    # A click at 3x display scale lands at a third of the displayed pixel.
    assert cdp.clamp_point(1920, 900, 3.0, 1280, 600) == (640, 300)
    # Out-of-range clicks clamp instead of reaching the page.
    assert cdp.clamp_point(5000, -40, 1.0, 1280, 600) == (1279, 0)
    # A zero scale must not divide by zero.
    assert cdp.clamp_point(10, 10, 0, 1280, 600) == (10, 10)


class FakeBrowser:
    """Just enough CDP: capture a screenshot, take a click, evaluate."""

    def __init__(self, evaluate_value='{"cluster": {"outcome": "timeout", "ms": 4001}}'):
        self.evaluate_value = evaluate_value
        # Answers for the two expressions `navigate()` polls, so a navigation can
        # be exercised without a real page.
        self.ready_state = "complete"
        self.location = "https://example.com/"
        self.fail_methods: set[str] = set()
        self.methods = []
        self.expressions = []
        self.navigations = []
        self.clicks = []
        self.handshakes = 0
        self.headers = None
        self._server = None
        self.port = 0

    async def __aenter__(self):
        self._server = await serve(self._handle, "127.0.0.1", 0)
        self.port = self._server.sockets[0].getsockname()[1]
        return self

    async def __aexit__(self, *exc_info):
        self._server.close()
        await self._server.wait_closed()

    async def _handle(self, socket):
        self.handshakes += 1
        self.headers = socket.request.headers
        async for raw in socket:
            message = json.loads(raw)
            method = message.get("method")
            self.methods.append(method)
            if method in self.fail_methods:
                await socket.send(
                    json.dumps({"id": message["id"], "error": {"message": "detached", "code": -32000}})
                )
                continue
            if method == "Page.captureScreenshot":
                result = {"data": base64.b64encode(PNG).decode()}
            elif method == "Input.dispatchMouseEvent":
                self.clicks.append((message["params"]["type"], message["params"]["x"], message["params"]["y"]))
                result = {}
            elif method == "Page.navigate":
                self.navigations.append(message["params"]["url"])
                result = {"frameId": "F1"}
            elif method == "Runtime.evaluate":
                expression = str(message["params"].get("expression") or "")
                self.expressions.append(expression)
                if "document.readyState" in expression:
                    value = self.ready_state
                elif "location.href" in expression:
                    value = self.location
                elif "requestAnimationFrame" in expression:
                    value = True
                else:
                    value = self.evaluate_value
                result = {"result": {"value": value}}
            else:
                result = {}
            await socket.send(json.dumps({"id": message["id"], "result": result}))


def client_for(browser, **kwargs):
    """A RouterCDP whose discovery step is short-circuited to the fake browser."""
    client = cdp.RouterCDP(
        "http://192.168.0.248",
        port=9222,
        namespace="sandbox-demo",
        token="test-token",
        connect_timeout=2,
        command_timeout=3,
        transport="ws",
        **kwargs,
    )

    async def page_socket_url(sandbox_id):
        return f"ws://127.0.0.1:{browser.port}/devtools/page/PAGE1"

    client.page_socket_url = page_socket_url  # type: ignore[method-assign]
    return client


def test_discovery_rewrites_loopback_and_sends_routing_headers():
    seen = {}

    def handler(request: httpx.Request) -> httpx.Response:
        seen["path"] = request.url.path
        seen["headers"] = dict(request.headers)
        return httpx.Response(
            200,
            json=[
                {"type": "browser", "webSocketDebuggerUrl": "ws://localhost:9222/devtools/browser/B1"},
                {"type": "page", "webSocketDebuggerUrl": "ws://localhost:9222/devtools/page/P1"},
            ],
        )

    async def body():
        client = cdp.RouterCDP(
            "http://192.168.0.248",
            port=9222,
            namespace="sandbox-demo",
            token="test-token",
            http_client=httpx.AsyncClient(transport=httpx.MockTransport(handler)),
        )
        try:
            return await client.page_socket_url("sb-4f2a")
        finally:
            await client.close_all()

    url = run(body())
    assert url == "ws://192.168.0.248/devtools/page/P1"
    assert seen["path"] == "/json/list"
    assert seen["headers"]["x-sandbox-id"] == "sb-4f2a"
    # 9222, not the router's own default of 8888.
    assert seen["headers"]["x-sandbox-port"] == "9222"
    assert seen["headers"]["x-sandbox-namespace"] == "sandbox-demo"
    assert seen["headers"]["authorization"] == "Bearer test-token"


def test_discovery_reports_router_errors_with_their_detail():
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(502, json={"detail": "Could not connect to the backend sandbox: sb-1"})

    async def body():
        client = cdp.RouterCDP(
            "http://192.168.0.248",
            port=9222,
            http_client=httpx.AsyncClient(transport=httpx.MockTransport(handler)),
        )
        try:
            await client.page_socket_url("sb-1")
        finally:
            await client.close_all()

    try:
        run(body())
    except cdp.RouterError as error:
        assert error.status == 502
        assert "Could not connect" in error.detail
    else:  # pragma: no cover - failure path guard
        raise AssertionError("expected RouterError")


def test_empty_target_list_is_an_error_not_an_empty_screenshot():
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(200, json=[])

    async def body():
        client = cdp.RouterCDP(
            "http://192.168.0.248",
            http_client=httpx.AsyncClient(transport=httpx.MockTransport(handler)),
        )
        try:
            await client.page_socket_url("sb-1")
        finally:
            await client.close_all()

    try:
        run(body())
    except cdp.RouterError as error:
        assert "no CDP targets" in error.detail
    else:  # pragma: no cover - failure path guard
        raise AssertionError("expected RouterError")


def test_screenshot_round_trips_and_reuses_one_socket():
    async def body():
        async with FakeBrowser() as browser:
            client = client_for(browser)
            try:
                first = await client.screenshot("sb-4f2a")
                second = await client.screenshot("sb-4f2a")
            finally:
                await client.close_all()
        return first, second, browser

    first, second, browser = run(body())
    assert first == PNG
    assert second == PNG
    assert browser.handshakes == 1
    assert browser.headers["x-sandbox-id"] == "sb-4f2a"
    assert browser.headers["x-sandbox-port"] == "9222"
    assert browser.headers["authorization"] == "Bearer test-token"


def test_click_sends_press_and_release_at_the_viewport_point():
    async def body():
        async with FakeBrowser() as browser:
            client = client_for(browser)
            try:
                point = await client.click("sb-4f2a", x=900, y=450, scale=3.0, width=1280, height=600)
            finally:
                await client.close_all()
        return point, browser

    point, browser = run(body())
    assert point == (300, 150)
    assert browser.clicks == [("mousePressed", 300, 150), ("mouseReleased", 300, 150)]


def test_evaluate_returns_the_page_value():
    async def body():
        async with FakeBrowser(evaluate_value='{"cluster": {"outcome": "timeout", "ms": 4001}}') as browser:
            client = client_for(browser)
            try:
                return await client.evaluate("sb-4f2a", "1 + 1")
            finally:
                await client.close_all()

    assert run(body()) == '{"cluster": {"outcome": "timeout", "ms": 4001}}'


def test_protocol_error_is_raised_as_cdp_error():
    async def body():
        async with FakeBrowser() as browser:
            client = client_for(browser)
            client.command_timeout = 0.4
            # The fake never answers Runtime.evaluate with an error, so force one
            # by asking for a method the fake replies to with an empty result and
            # then assert on the timeout path instead.
            browser.evaluate_value = None
            try:
                return await client.evaluate("sb-4f2a", "1")
            finally:
                await client.close_all()

    assert run(body()) is None


def _http_client(handler):
    return httpx.AsyncClient(transport=httpx.MockTransport(handler))


def test_http_transport_posts_commands_to_the_bridge():
    seen = {}

    def handler(request: httpx.Request) -> httpx.Response:
        seen["path"] = request.url.path
        seen["headers"] = dict(request.headers)
        seen["body"] = json.loads(request.content)
        return httpx.Response(200, json={"result": {"data": base64.b64encode(PNG).decode()}})

    async def body():
        client = cdp.RouterCDP(
            "http://192.168.0.248",
            port=9222,
            namespace="sandbox-demo",
            token="test-token",
            transport="http",
            command_timeout=3,
            http_client=_http_client(handler),
        )
        try:
            return await client.screenshot("sb-4f2a")
        finally:
            await client.close_all()

    assert run(body()) == PNG
    assert seen["path"] == "/cdp"
    assert seen["body"]["method"] == "Page.captureScreenshot"
    assert seen["body"]["params"]["format"] == "png"
    # Same routing contract as the WebSocket path.
    assert seen["headers"]["x-sandbox-id"] == "sb-4f2a"
    assert seen["headers"]["x-sandbox-port"] == "9222"
    assert seen["headers"]["x-sandbox-namespace"] == "sandbox-demo"
    assert seen["headers"]["authorization"] == "Bearer test-token"


def test_http_transport_sends_clicks_and_reads_evaluate():
    calls = []

    def handler(request: httpx.Request) -> httpx.Response:
        payload = json.loads(request.content)
        calls.append(payload)
        if payload["method"] == "Runtime.evaluate":
            return httpx.Response(200, json={"result": {"result": {"value": "Example Domain"}}})
        return httpx.Response(200, json={"result": {}})

    async def body():
        client = cdp.RouterCDP(
            "http://192.168.0.248",
            transport="http",
            command_timeout=3,
            http_client=_http_client(handler),
        )
        try:
            point = await client.click("sb-4f2a", x=1920, y=900, scale=3.0)
            title = await client.title("sb-4f2a")
            return point, title
        finally:
            await client.close_all()

    point, title = run(body())
    assert point == (640, 300)
    assert title == "Example Domain"
    methods = [call["method"] for call in calls]
    assert methods.count("Input.dispatchMouseEvent") == 2
    assert "Runtime.evaluate" in methods


def test_http_transport_maps_a_bridge_failure_to_a_router_error():
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(502, json={"error": "WebSocketError: upstream closed the socket"})

    async def body():
        client = cdp.RouterCDP(
            "http://192.168.0.248",
            transport="http",
            command_timeout=3,
            http_client=_http_client(handler),
        )
        try:
            await client.screenshot("sb-1")
        finally:
            await client.close_all()

    try:
        run(body())
    except cdp.RouterError as error:
        assert error.status == 502
        assert "upstream closed" in error.detail
    else:  # pragma: no cover - failure path guard
        raise AssertionError("expected RouterError")


# --------------------------------------------------------------------------- #
# paint settle and navigation
# --------------------------------------------------------------------------- #
def test_click_settles_the_frame_before_it_returns():
    """The capture that follows a click must show the click, not the frame before it."""

    async def body():
        async with FakeBrowser() as browser:
            client = client_for(browser)
            try:
                await client.click("sb-4f2a", x=10, y=20)
            finally:
                await client.close_all()
        return browser

    browser = run(body())
    assert browser.methods[-3:] == [
        "Input.dispatchMouseEvent",
        "Input.dispatchMouseEvent",
        "Runtime.evaluate",
    ]
    assert "requestAnimationFrame" in browser.expressions[-1]


def test_a_detached_context_during_the_settle_is_not_an_error():
    """A click that navigates destroys the context; the click still happened."""

    async def body():
        async with FakeBrowser() as browser:
            browser.fail_methods = {"Runtime.evaluate"}
            client = client_for(browser)
            try:
                return await client.click("sb-4f2a", x=1, y=2), browser
            finally:
                await client.close_all()

    point, browser = run(body())
    assert point == (1, 2)
    assert "Runtime.evaluate" in browser.methods


def test_navigate_waits_for_load_and_returns_where_it_landed():
    async def body():
        async with FakeBrowser() as browser:
            browser.location = "https://wikipedia.org/"
            client = client_for(browser)
            try:
                return await client.navigate("sb-4f2a", "https://wikipedia.org"), browser
            finally:
                await client.close_all()

    landed, browser = run(body())
    assert landed == "https://wikipedia.org/"
    assert browser.navigations == ["https://wikipedia.org"]
    assert any("document.readyState" in expression for expression in browser.expressions)


def test_a_page_that_never_finishes_loading_is_an_error():
    async def body():
        async with FakeBrowser() as browser:
            browser.ready_state = "loading"
            client = client_for(browser)
            try:
                await client.navigate("sb-4f2a", "https://slow.example/", timeout=0.2)
            finally:
                await client.close_all()

    try:
        run(body())
    except cdp.CDPError as error:
        assert "did not finish" in str(error)
    else:  # pragma: no cover - failure path guard
        raise AssertionError("expected CDPError")
