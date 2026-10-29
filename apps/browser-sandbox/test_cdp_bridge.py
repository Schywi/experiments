"""Tests for the in-pod HTTP bridge.

No Chromium and no Playwright: the WebSocket client is exercised against a real
`websockets` server, and the HTTP surface against a fake DevTools that records
the commands it is asked to run.
"""

import asyncio
import importlib.util
import json
import socket
import time
from pathlib import Path

import httpx
from websockets.asyncio.server import serve

HERE = Path(__file__).parent
SPEC = importlib.util.spec_from_file_location("cdp_bridge", HERE / "cdp-bridge.py")
bridge = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(bridge)


def run(coro):
    return asyncio.run(coro)


def _free_port() -> int:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


class FakeDevTools:
    """Stands in for the Chromium client: records calls, returns canned results."""

    def __init__(self, result=None, error=None):
        self.calls = []
        self.dropped = 0
        self._result = result if result is not None else {"ok": True}
        self._error = error

    async def call(self, method, params=None):
        self.calls.append((method, params or {}))
        if self._error:
            raise bridge.WebSocketError(self._error)
        return self._result

    def drop(self):
        self.dropped += 1


def test_websocket_client_speaks_to_a_real_server():
    async def body():
        async def handler(socket):
            async for message in socket:
                payload = json.loads(message)
                await socket.send(json.dumps({"id": payload["id"], "result": {"echo": payload["method"]}}))

        server = await serve(handler, "127.0.0.1", 0)
        port = server.sockets[0].getsockname()[1]
        try:
            socket = await bridge.DevToolsSocket.connect("127.0.0.1", port, "/devtools/page/X")
            await socket.send_text(json.dumps({"id": 1, "method": "Page.enable", "params": {}}))
            reply = json.loads(await asyncio.wait_for(socket.recv_text(), timeout=5))
            socket.close()
            return reply
        finally:
            server.close()
            await server.wait_closed()

    assert run(body()) == {"id": 1, "result": {"echo": "Page.enable"}}


def test_devtools_call_skips_events_and_returns_the_matching_id():
    async def body():
        async def handler(socket):
            async for message in socket:
                payload = json.loads(message)
                # An event first: the console never subscribes, so it must be skipped.
                await socket.send(json.dumps({"method": "Page.loadEventFired", "params": {}}))
                await socket.send(json.dumps({"id": payload["id"], "result": {"data": "AAAA"}}))

        server = await serve(handler, "127.0.0.1", 0)
        port = server.sockets[0].getsockname()[1]
        devtools = bridge.DevTools("127.0.0.1", port, timeout=5)

        async def page_path():
            return "/devtools/page/X"

        devtools._page_path = page_path  # type: ignore[method-assign]
        try:
            return await devtools.call("Page.captureScreenshot", {"format": "png"})
        finally:
            devtools.drop()
            server.close()
            await server.wait_closed()

    assert run(body()) == {"data": "AAAA"}


def test_devtools_reports_a_protocol_error():
    async def body():
        async def handler(socket):
            async for message in socket:
                payload = json.loads(message)
                await socket.send(json.dumps({"id": payload["id"], "error": {"message": "no page"}}))

        server = await serve(handler, "127.0.0.1", 0)
        port = server.sockets[0].getsockname()[1]
        devtools = bridge.DevTools("127.0.0.1", port, timeout=5)

        async def page_path():
            return "/devtools/page/X"

        devtools._page_path = page_path  # type: ignore[method-assign]
        try:
            await devtools.call("Page.enable")
        finally:
            devtools.drop()
            server.close()
            await server.wait_closed()

    try:
        run(body())
    except bridge.WebSocketError as error:
        assert "no page" in str(error)
    else:  # pragma: no cover - failure path guard
        raise AssertionError("expected WebSocketError")


def _serve_http(devtools):
    async def start():
        server = await asyncio.start_server(
            lambda reader, writer: bridge.handle(reader, writer, devtools), "127.0.0.1", 0
        )
        port = server.sockets[0].getsockname()[1]
        return server, port

    return start


def test_post_cdp_forwards_the_command_and_wraps_the_result():
    devtools = FakeDevTools(result={"data": "UE5H"})

    async def body():
        server, port = await _serve_http(devtools)()
        try:
            async with httpx.AsyncClient() as client:
                response = await client.post(
                    f"http://127.0.0.1:{port}/cdp",
                    json={"method": "Page.captureScreenshot", "params": {"format": "png"}},
                )
            return response, devtools
        finally:
            server.close()
            await server.wait_closed()

    response, devtools = run(body())
    assert response.status_code == 200
    assert response.json() == {"result": {"data": "UE5H"}}
    assert devtools.calls == [("Page.captureScreenshot", {"format": "png"})]


def test_post_cdp_rejects_a_missing_method():
    devtools = FakeDevTools()

    async def body():
        server, port = await _serve_http(devtools)()
        try:
            async with httpx.AsyncClient() as client:
                return await client.post(f"http://127.0.0.1:{port}/cdp", json={})
        finally:
            server.close()
            await server.wait_closed()

    response = run(body())
    assert response.status_code == 400
    assert "method is required" in response.text
    assert devtools.calls == []


def test_post_cdp_reports_an_upstream_failure_and_drops_the_socket():
    devtools = FakeDevTools(error="upstream closed the socket")

    async def body():
        server, port = await _serve_http(devtools)()
        try:
            async with httpx.AsyncClient() as client:
                return await client.post(
                    f"http://127.0.0.1:{port}/cdp", json={"method": "Page.enable"}
                )
        finally:
            server.close()
            await server.wait_closed()

    response = run(body())
    assert response.status_code == 502
    assert "upstream closed the socket" in response.text
    # The next request must reconnect rather than reuse a half-open socket.
    assert devtools.dropped == 1


def test_healthz_and_json_list_proxy(monkeypatch):
    devtools = FakeDevTools()
    proxied = {}

    async def fake_http_get(host, port, path, timeout=5.0):
        proxied[path] = (host, port)
        return 200, json.dumps([{"type": "page", "webSocketDebuggerUrl": "ws://127.0.0.1:9223/devtools/page/X"}]).encode()

    monkeypatch.setattr(bridge, "http_get", fake_http_get)

    async def body():
        server, port = await _serve_http(devtools)()
        try:
            async with httpx.AsyncClient() as client:
                health = await client.get(f"http://127.0.0.1:{port}/healthz")
                listing = await client.get(f"http://127.0.0.1:{port}/json/list")
            return health, listing
        finally:
            server.close()
            await server.wait_closed()

    health, listing = run(body())
    assert health.status_code == 200 and health.json() == {"ok": True}
    assert listing.status_code == 200
    assert listing.json()[0]["type"] == "page"
    assert "/json/list" in proxied


def test_entrypoint_starts_chromium_on_loopback_and_the_bridge_on_the_pod_ip():
    script = (HERE / "entrypoint.sh").read_text()
    assert 'CDP_UPSTREAM_PORT="${CDP_UPSTREAM_PORT:-9223}"' in script
    assert '--remote-debugging-port="${CDP_UPSTREAM_PORT}"' in script
    assert "--remote-debugging-address=" not in script
    # The bridge owns the published port; Chromium must not.
    assert "python3 /usr/local/bin/cdp-bridge" in script
    assert "trap cleanup TERM INT" in script


def test_http_get_reads_by_length_when_chromium_keeps_the_socket_open():
    """Chromium ignores `Connection: close`; reading to EOF would hang.

    This is the bug that made every sandbox fail its readiness probe the first
    time the bridge shipped: the proxy waited for EOF until the timeout.
    """
    payload = b'{"Browser":"Chrome/130.0.6723.31"}'

    async def body():
        async def handler(reader, writer):
            await reader.readuntil(b"\r\n\r\n")
            writer.write(
                b"HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: "
                + str(len(payload)).encode()
                + b"\r\n\r\n"
                + payload
            )
            await writer.drain()
            await asyncio.sleep(3)  # keeps the socket open, like Chromium does
            writer.close()

        server = await asyncio.start_server(handler, "127.0.0.1", 0)
        port = server.sockets[0].getsockname()[1]
        started = time.monotonic()
        try:
            status, body_bytes = await bridge.http_get("127.0.0.1", port, "/json/version", timeout=5)
            elapsed = time.monotonic() - started
        finally:
            # wait_closed() would block on the handler's own 3 s sleep, so the
            # timing above is taken before the teardown.
            server.close()
        return status, body_bytes, elapsed

    status, body_bytes, elapsed = run(body())
    assert status == 200
    assert body_bytes == payload
    assert elapsed < 1.5, f"http_get waited {elapsed:.1f}s for an EOF that never comes"


def test_json_version_proxies_a_keep_alive_upstream(monkeypatch):
    async def fake_http_get(host, port, path, timeout=5.0):
        return 200, b'{"Browser":"Chrome/130"}'

    monkeypatch.setattr(bridge, "http_get", fake_http_get)

    async def body():
        server, port = await _serve_http(FakeDevTools())()
        try:
            async with httpx.AsyncClient() as client:
                return await client.get(f"http://127.0.0.1:{port}/json/version")
        finally:
            server.close()
            await server.wait_closed()

    response = run(body())
    assert response.status_code == 200
    assert response.json()["Browser"] == "Chrome/130"
