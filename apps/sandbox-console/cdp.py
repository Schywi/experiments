"""Chrome DevTools Protocol over the sandbox-router.

Why the router at all: a template-derived sandbox pod drops every ingress peer
except pods labelled `app=sandbox-router` in `agent-sandbox-system`
(`extensions/controllers/sandboxtemplate_controller.go`). The pod IP is not
reachable from this console, so every CDP byte goes through the router with the
`X-Sandbox-*` headers.

Two details decide whether this works:

1. Chromium's `/json/list` answers with a *loopback* socket URL
   (`ws://localhost:9222/devtools/page/<id>`). Dialling that from another pod
   reaches nothing, so the host and port are rewritten to the router's and the
   routing headers ride on the WebSocket handshake. The router forwards protocol
   upgrades transparently and strips `Origin`, which is what makes this legal
   from a non-browser client.
2. `X-Sandbox-Port` must be sent explicitly: the router's default is 8888, not
   the 9222 Chromium listens on.

All calls for one sandbox are serialised behind a lock, because CDP multiplexes
replies over a single socket by message id and interleaving two callers would
not corrupt the stream but would make timeouts hard to attribute.
"""

from __future__ import annotations

import asyncio
import base64
import itertools
import json
import time
from dataclasses import dataclass, field
from typing import Any
from urllib.parse import urlparse, urlunparse

import httpx
import websockets


class RouterError(Exception):
    """The router refused or could not reach the sandbox."""

    def __init__(self, status: int, detail: str):
        super().__init__(f"router {status}: {detail}")
        self.status = status
        self.detail = detail


class CDPError(Exception):
    """The browser answered a command with a protocol error."""


def rewrite_ws_url(router_url: str, target_url: str) -> str:
    """Point a loopback devtools URL at the router, keeping its path.

    `ws://localhost:9222/devtools/page/AB` becomes
    `ws://192.168.0.248/devtools/page/AB` when the router is
    `http://192.168.0.248`. The port is dropped on purpose: the router resolves
    the upstream port from `X-Sandbox-Port`.
    """
    router = urlparse(router_url)
    target = urlparse(target_url)
    scheme = "wss" if router.scheme == "https" else "ws"
    # urlunparse order is (scheme, netloc, path, params, query, fragment); the
    # devtools path never carries a `;params` segment, so that slot stays empty.
    return urlunparse((scheme, router.netloc, target.path or "/", "", target.query, ""))


def clamp_point(x: float, y: float, scale: float, width: int, height: int) -> tuple[int, int]:
    """Map a click in a scaled-up image back to viewport pixels.

    `scale` is `displayed_px / viewport_px`. Coordinates are clamped to the
    viewport so a click on the letterboxed edge cannot land outside the page.
    """
    factor = scale if scale and scale > 0 else 1.0
    viewport_x = (float(x) / factor)
    viewport_y = (float(y) / factor)
    clamped_x = min(max(int(round(viewport_x)), 0), max(width - 1, 0))
    clamped_y = min(max(int(round(viewport_y)), 0), max(height - 1, 0))
    return clamped_x, clamped_y


@dataclass
class _Session:
    socket: Any
    lock: asyncio.Lock = field(default_factory=asyncio.Lock)
    ids: Any = field(default_factory=lambda: itertools.count(1))
    page_enabled: bool = False


class RouterCDP:
    """A CDP client whose transport is the sandbox-router."""

    def __init__(
        self,
        router_url: str,
        port: int = 9222,
        namespace: str | None = None,
        token: str = "",
        connect_timeout: float = 5.0,
        command_timeout: float = 10.0,
        viewport: tuple[int, int] = (1280, 600),
        transport: str = "http",
        http_client: httpx.AsyncClient | None = None,
    ):
        self.router_url = router_url.rstrip("/")
        self.port = port
        self.namespace = namespace
        self.token = token
        self.connect_timeout = connect_timeout
        self.command_timeout = command_timeout
        self.viewport = viewport
        # "http" goes through the in-pod bridge; "ws" dials Chromium directly and
        # needs a router that can forward upgrades.
        self.transport = (transport or "http").lower()
        self._http = http_client
        self._owns_http = http_client is None
        self._sessions: dict[str, _Session] = {}
        # Sandboxes whose Page domain and viewport are already set up. In http
        # mode there is no per-sandbox socket to hang that flag on.
        self._enabled: set[str] = set()

    # -- transport ---------------------------------------------------------

    def headers(self, sandbox_id: str) -> dict[str, str]:
        headers = {"X-Sandbox-ID": sandbox_id, "X-Sandbox-Port": str(self.port)}
        if self.namespace:
            headers["X-Sandbox-Namespace"] = self.namespace
        if self.token:
            headers["Authorization"] = f"Bearer {self.token}"
        return headers

    async def _client(self) -> httpx.AsyncClient:
        if self._http is None:
            self._http = httpx.AsyncClient(timeout=self.connect_timeout)
        return self._http

    async def list_targets(self, sandbox_id: str) -> list[dict]:
        client = await self._client()
        url = f"{self.router_url}/json/list"
        try:
            response = await client.get(url, headers=self.headers(sandbox_id))
        except httpx.HTTPError as exc:
            raise RouterError(0, f"unreachable: {exc.__class__.__name__}") from exc
        if response.status_code != 200:
            detail = ""
            try:
                detail = str((response.json() or {}).get("detail", ""))
            except ValueError:
                detail = response.text[:200]
            raise RouterError(response.status_code, detail or response.reason_phrase)
        try:
            targets = response.json()
        except ValueError as exc:
            raise RouterError(200, f"non-JSON /json/list: {exc}") from exc
        return [item for item in targets if isinstance(item, dict)]

    async def page_socket_url(self, sandbox_id: str) -> str:
        """The router-facing WebSocket URL of the first page target."""
        targets = await self.list_targets(sandbox_id)
        pages = [item for item in targets if item.get("type") == "page"] or targets
        if not pages:
            raise RouterError(200, "no CDP targets (browser not up yet)")
        raw = str(pages[0].get("webSocketDebuggerUrl") or "")
        if not raw:
            raise RouterError(200, "target has no webSocketDebuggerUrl")
        return rewrite_ws_url(self.router_url, raw)

    async def _session(self, sandbox_id: str) -> _Session:
        existing = self._sessions.get(sandbox_id)
        if existing is not None:
            return existing
        url = await self.page_socket_url(sandbox_id)
        headers = self.headers(sandbox_id)
        socket = await self._connect(url, headers)
        session = _Session(socket=socket)
        self._sessions[sandbox_id] = session
        return session

    async def _connect(self, url: str, headers: dict[str, str]):
        """Open the socket, tolerating the `websockets` header-kwarg rename."""
        kwargs = {
            "open_timeout": self.connect_timeout,
            "close_timeout": 1,
            "max_size": 64 * 1024 * 1024,
            "ping_interval": None,
        }
        try:
            return await websockets.connect(url, additional_headers=headers, **kwargs)
        except TypeError:
            # websockets < 14 spells it `extra_headers`.
            return await websockets.connect(url, extra_headers=headers, **kwargs)

    async def call(self, sandbox_id: str, method: str, params: dict | None = None) -> dict:
        """Issue one CDP command and return its result."""
        if self.transport == "http":
            return await self._call_http(sandbox_id, method, params or {})
        session = await self._session(sandbox_id)
        async with session.lock:
            try:
                return await asyncio.wait_for(
                    self._roundtrip(session, method, params or {}), self.command_timeout
                )
            except asyncio.TimeoutError as exc:
                await self.close(sandbox_id)
                raise CDPError(f"{method} timed out after {self.command_timeout}s") from exc
            except (websockets.exceptions.WebSocketException, OSError) as exc:
                await self.close(sandbox_id)
                raise CDPError(f"{method} socket failed: {exc.__class__.__name__}") from exc

    async def _call_http(self, sandbox_id: str, method: str, params: dict) -> dict:
        """POST the command to the in-pod bridge.

        The bridge owns the WebSocket to Chromium; the router only ever sees
        plain HTTP, which it forwards correctly. A 502 here means the bridge
        could not reach the browser, not that routing failed.
        """
        client = await self._client()
        try:
            response = await client.post(
                f"{self.router_url}/cdp",
                headers={**self.headers(sandbox_id), "Content-Type": "application/json"},
                json={"method": method, "params": params},
                timeout=self.command_timeout,
            )
        except httpx.HTTPError as exc:
            raise RouterError(0, f"unreachable: {exc.__class__.__name__}") from exc
        payload: dict = {}
        try:
            payload = response.json() or {}
        except ValueError:
            payload = {}
        if response.status_code != 200:
            detail = str(payload.get("error") or payload.get("detail") or response.text[:200])
            raise RouterError(response.status_code, detail or response.reason_phrase)
        if "error" in payload:
            raise CDPError(str(payload["error"]))
        return payload.get("result") or {}

    async def _roundtrip(self, session: _Session, method: str, params: dict) -> dict:
        message_id = next(session.ids)
        await session.socket.send(json.dumps({"id": message_id, "method": method, "params": params}))
        while True:
            raw = await session.socket.recv()
            try:
                message = json.loads(raw)
            except (TypeError, ValueError):
                continue
            if message.get("id") != message_id:
                continue  # an event; nothing here subscribes to them yet
            if "error" in message:
                error = message["error"] or {}
                raise CDPError(str(error.get("message") or error))
            return message.get("result") or {}

    # -- commands ----------------------------------------------------------

    async def screenshot(self, sandbox_id: str) -> bytes:
        """One PNG frame of the current page."""
        await self._enable_page(sandbox_id)
        result = await self.call(
            sandbox_id,
            "Page.captureScreenshot",
            {"format": "png", "captureBeyondViewport": False, "fromSurface": True},
        )
        data = result.get("data")
        if not data:
            raise CDPError("captureScreenshot returned no data")
        return base64.b64decode(data)

    async def click(self, sandbox_id: str, x: float, y: float, scale: float = 1.0, width: int | None = None, height: int | None = None) -> tuple[int, int]:
        """Press and release at a viewport coordinate. Returns the clamped point.

        The release is followed by `settle()`, so a capture taken right after
        this returns shows the click's effect instead of the frame before it.
        """
        await self._enable_page(sandbox_id)
        width = width or self.viewport[0]
        height = height or self.viewport[1]
        point_x, point_y = clamp_point(x, y, scale, width, height)
        for event_type in ("mousePressed", "mouseReleased"):
            await self.call(
                sandbox_id,
                "Input.dispatchMouseEvent",
                {
                    "type": event_type,
                    "x": point_x,
                    "y": point_y,
                    "button": "left",
                    "buttons": 1 if event_type == "mousePressed" else 0,
                    "clickCount": 1,
                },
            )
        await self.settle(sandbox_id)
        return point_x, point_y

    async def settle(self, sandbox_id: str, frames: int = 2) -> None:
        """Wait for the compositor to present the frame an input event produced.

        `Page.captureScreenshot` returns the last presented surface. A mouse
        release is dispatched, the page reacts, and only then does a frame
        exist, so capturing straight after the release returns the picture from
        *before* the click. Two animation frames are enough for Chromium to
        paint the result.

        A click that navigates destroys the execution context, so a failure here
        is expected rather than an error: the caller captures whatever the new
        page shows. The expression resolves `false` when the document has no
        animation frames to wait on, which some error pages do.
        """
        expression = (
            "new Promise(function (resolve) {"
            "  if (typeof requestAnimationFrame !== 'function') { resolve(false); return; }"
            "  var left = %d;"
            "  function step() {"
            "    left -= 1;"
            "    if (left <= 0) { resolve(true); return; }"
            "    requestAnimationFrame(step);"
            "  }"
            "  requestAnimationFrame(step);"
            "})" % max(1, int(frames))
        )
        try:
            await self.evaluate(sandbox_id, expression, await_promise=True)
        except (CDPError, RouterError):
            return

    async def navigate(self, sandbox_id: str, url: str, timeout: float | None = None) -> str:
        """Open `url` in the sandbox's page and wait until it has settled.

        Returns the page's `location.href` after load, which is the value the
        card shows: a redirect or a `https` upgrade is what the browser really
        ended up on, not what was asked for.

        There are no CDP events over the HTTP bridge (see the module docstring),
        so "loaded" is polled from the page rather than waited for.
        """
        await self._enable_page(sandbox_id)
        await self.call(sandbox_id, "Page.navigate", {"url": url})
        budget = self.command_timeout if timeout is None else timeout
        deadline = time.monotonic() + max(0.0, budget)
        while time.monotonic() < deadline:
            try:
                if await self.evaluate(sandbox_id, "document.readyState") == "complete":
                    await self.settle(sandbox_id)
                    return str(await self.evaluate(sandbox_id, "location.href") or url)
            except CDPError:
                # The old context is gone mid-navigation; ask again shortly.
                pass
            await asyncio.sleep(0.05)
        raise CDPError(f"navigation to {url} did not finish in {budget:.1f}s")

    async def evaluate(self, sandbox_id: str, expression: str, await_promise: bool = True) -> Any:
        """Run JavaScript in the page and return its value."""
        result = await self.call(
            sandbox_id,
            "Runtime.evaluate",
            {
                "expression": expression,
                "awaitPromise": await_promise,
                "returnByValue": True,
            },
        )
        if result.get("exceptionDetails"):
            details = result["exceptionDetails"]
            raise CDPError(str(details.get("text") or details))
        return (result.get("result") or {}).get("value")

    async def title(self, sandbox_id: str) -> str:
        return str(await self.evaluate(sandbox_id, "document.title") or "")

    async def location(self, sandbox_id: str) -> str:
        return str(await self.evaluate(sandbox_id, "location.href") or "")

    async def _enable_page(self, sandbox_id: str) -> None:
        if sandbox_id in self._enabled:
            return
        try:
            await self.call(sandbox_id, "Page.enable", {})
        except CDPError:
            pass  # captureScreenshot works on some builds without Page.enable
        # Pin the viewport. The window the entrypoint opened is only a hint; this
        # is what makes the 1280x600 the console divides click coordinates by
        # the viewport the page actually has.
        try:
            await self.call(
                sandbox_id,
                "Emulation.setDeviceMetricsOverride",
                {
                    "width": self.viewport[0],
                    "height": self.viewport[1],
                    "deviceScaleFactor": 1,
                    "mobile": False,
                },
            )
        except CDPError:
            pass
        self._enabled.add(sandbox_id)
        if self.transport == "ws":
            session = self._sessions.get(sandbox_id)
            if session is not None:
                session.page_enabled = True

    async def close(self, sandbox_id: str) -> None:
        self._enabled.discard(sandbox_id)
        session = self._sessions.pop(sandbox_id, None)
        if session is None:
            return
        try:
            await session.socket.close()
        except Exception:  # noqa: BLE001 - closing a dead socket is not an error
            pass

    async def close_all(self) -> None:
        for sandbox_id in list(self._sessions):
            await self.close(sandbox_id)
        if self._owns_http and self._http is not None:
            await self._http.aclose()
            self._http = None
