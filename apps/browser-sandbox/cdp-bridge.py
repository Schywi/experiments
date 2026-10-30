"""HTTP shim in front of Chromium's loopback-only DevTools endpoint.

Why this exists, in one paragraph: the DevTools protocol is WebSocket-only, the
sandbox-router cannot forward a WebSocket (`sandbox-router/proxy/proxy.go:545`
wraps every upstream response body in a counting wrapper, so Go's ReverseProxy
aborts the 101 with "101 switching protocols response with non-writable body"),
and Chromium 130 binds its DevTools port to 127.0.0.1 whatever
`--remote-debugging-address` says. The console is therefore given plain HTTP on
the pod IP, and this process holds the WebSocket itself:

    console --HTTP--> router --HTTP--> podIP:9222 --WS--> 127.0.0.1:9223 Chromium

Routes:

    GET  /json/version   proxied to Chromium (the SandboxTemplate readiness probe)
    GET  /json/list      proxied to Chromium
    POST /cdp            {"method": "...", "params": {...}} -> {"result": {...}}
    GET  /healthz        {"ok": true}

No third-party packages: a minimal WebSocket client (Chrome's framing, no
extensions) and an asyncio HTTP server are all that is needed, and the Playwright
image already carries the interpreter.
"""

from __future__ import annotations

import argparse
import asyncio
import base64
import json
import os
import struct
import sys

CHROMIUM_HOST = os.environ.get("CDP_UPSTREAM_HOST", "127.0.0.1")
CHROMIUM_PORT = int(os.environ.get("CDP_UPSTREAM_PORT", "9223"))
LISTEN_PORT = int(os.environ.get("CDP_PORT", "9222"))
COMMAND_TIMEOUT = float(os.environ.get("CDP_BRIDGE_TIMEOUT", "20"))


# --------------------------------------------------------------------------- #
# Minimal WebSocket client (RFC 6455, client side, text frames only)
# --------------------------------------------------------------------------- #
class WebSocketError(Exception):
    """The upstream DevTools socket failed."""


class DevToolsSocket:
    """One text-frame WebSocket to Chromium's DevTools listener."""

    def __init__(self, reader: asyncio.StreamReader, writer: asyncio.StreamWriter):
        self._reader = reader
        self._writer = writer

    @classmethod
    async def connect(cls, host: str, port: int, path: str, timeout: float = 5.0) -> "DevToolsSocket":
        reader, writer = await asyncio.wait_for(asyncio.open_connection(host, port), timeout=timeout)
        key = base64.b64encode(os.urandom(16)).decode()
        request = (
            f"GET {path} HTTP/1.1\r\n"
            f"Host: {host}:{port}\r\n"
            "Upgrade: websocket\r\n"
            "Connection: Upgrade\r\n"
            f"Sec-WebSocket-Key: {key}\r\n"
            "Sec-WebSocket-Version: 13\r\n"
            "\r\n"
        )
        writer.write(request.encode())
        await writer.drain()
        head = await asyncio.wait_for(reader.readuntil(b"\r\n\r\n"), timeout=timeout)
        status_line = head.split(b"\r\n", 1)[0].decode("latin-1")
        if "101" not in status_line:
            writer.close()
            raise WebSocketError(f"upgrade refused: {status_line}")
        return cls(reader, writer)

    async def send_text(self, payload: str) -> None:
        data = payload.encode()
        header = bytearray([0x81])  # FIN + text
        length = len(data)
        if length < 126:
            header.append(0x80 | length)
        elif length < (1 << 16):
            header.append(0x80 | 126)
            header += struct.pack("!H", length)
        else:
            header.append(0x80 | 127)
            header += struct.pack("!Q", length)
        mask = os.urandom(4)
        header += mask
        masked = bytes(byte ^ mask[index % 4] for index, byte in enumerate(data))
        self._writer.write(bytes(header) + masked)
        await self._writer.drain()

    async def recv_text(self) -> str:
        """Read one complete text message, answering pings and ignoring binary."""
        fragments = []
        while True:
            first, second = await self._read_exactly(2)
            opcode = first & 0x0F
            masked = bool(second & 0x80)
            length = second & 0x7F
            if length == 126:
                (length,) = struct.unpack("!H", await self._read_exactly(2))
            elif length == 127:
                (length,) = struct.unpack("!Q", await self._read_exactly(8))
            mask = await self._read_exactly(4) if masked else b""
            payload = await self._read_exactly(length) if length else b""
            if masked:
                payload = bytes(byte ^ mask[index % 4] for index, byte in enumerate(payload))
            if opcode == 0x8:  # close
                raise WebSocketError("upstream closed the socket")
            if opcode == 0x9:  # ping -> pong
                self._writer.write(bytes([0x8A, 0x80]) + os.urandom(4))
                await self._writer.drain()
                continue
            if opcode == 0xA:  # pong
                continue
            if opcode in (0x0, 0x1):  # continuation / text
                fragments.append(payload)
                if first & 0x80:  # FIN
                    return b"".join(fragments).decode()
                continue
            # binary frames are not part of CDP over text; skip them
            if first & 0x80:
                fragments.clear()

    async def _read_exactly(self, count: int) -> bytes:
        data = await self._reader.readexactly(count)
        return data

    def close(self) -> None:
        try:
            self._writer.close()
        except Exception:  # noqa: BLE001 - closing twice is not an error
            pass


# --------------------------------------------------------------------------- #
# Chromium plumbing
# --------------------------------------------------------------------------- #
async def http_get(host: str, port: int, path: str, timeout: float = 5.0) -> tuple[int, bytes]:
    """Tiny HTTP/1.1 GET, enough for Chromium's /json endpoints.

    Chromium answers with `Content-Length` and keeps the connection open even
    when asked for `Connection: close`, so the body is read by length rather than
    until EOF. Reading until EOF hangs until the timeout, which turns into a
    failing readiness probe -- that is how this was found.
    """
    reader, writer = await asyncio.wait_for(asyncio.open_connection(host, port), timeout=timeout)
    try:
        writer.write(
            (
                f"GET {path} HTTP/1.1\r\n"
                f"Host: {host}:{port}\r\n"
                "Accept: application/json\r\n"
                "Connection: close\r\n"
                "\r\n"
            ).encode()
        )
        await writer.drain()
        head = await asyncio.wait_for(reader.readuntil(b"\r\n\r\n"), timeout=timeout)
        status = int(head.split(b" ", 2)[1])
        headers = {}
        for line in head.split(b"\r\n")[1:]:
            if b":" in line:
                key, _, value = line.partition(b":")
                headers[key.strip().lower()] = value.strip()
        length_header = headers.get(b"content-length")
        if length_header is not None:
            body = await asyncio.wait_for(reader.readexactly(int(length_header)), timeout=timeout)
        elif headers.get(b"transfer-encoding", b"").lower() == b"chunked":
            body = await _read_chunked(reader, timeout)
        else:  # no length: read what arrives, then stop instead of hanging
            body = await asyncio.wait_for(reader.read(1 << 20), timeout=timeout)
    finally:
        writer.close()
    return status, body


async def _read_chunked(reader: asyncio.StreamReader, timeout: float) -> bytes:
    chunks = []
    while True:
        line = await asyncio.wait_for(reader.readuntil(b"\r\n"), timeout=timeout)
        size = int(line.strip() or b"0", 16)
        if size == 0:
            await asyncio.wait_for(reader.readuntil(b"\r\n"), timeout=timeout)
            return b"".join(chunks)
        chunks.append(await asyncio.wait_for(reader.readexactly(size), timeout=timeout))
        await asyncio.wait_for(reader.readexactly(2), timeout=timeout)  # CRLF


# Domains Chromium only accepts on the browser endpoint. Target.createBrowserContext
# on a page socket fails with "Not allowed", which is what a shared browser needs.
BROWSER_DOMAINS = ("Target.", "Browser.", "SystemInfo.")


class DevTools:
    """Cached DevTools sockets, serialised, with a reconnect on failure.

    Two endpoints: a page socket for Page./Runtime. and the browser socket for
    Target., which is where browser contexts live.
    """

    def __init__(self, host: str, port: int, timeout: float = COMMAND_TIMEOUT):
        self._host = host
        self._port = port
        self._timeout = timeout
        self._socket: DevToolsSocket | None = None
        self._browser: DevToolsSocket | None = None
        self._lock = asyncio.Lock()
        self._ids = 0

    async def _page_path(self) -> str:
        status, body = await http_get(self._host, self._port, "/json/list")
        if status != 200:
            raise WebSocketError(f"/json/list returned {status}")
        targets = json.loads(body or b"[]")
        pages = [item for item in targets if item.get("type") == "page"] or targets
        if not pages:
            raise WebSocketError("no CDP targets")
        url = pages[0].get("webSocketDebuggerUrl") or ""
        if "://" not in url:
            raise WebSocketError("target has no webSocketDebuggerUrl")
        # ws://127.0.0.1:9223/devtools/page/ABC -> /devtools/page/ABC
        return "/" + url.split("://", 1)[1].split("/", 1)[1]

    async def _browser_path(self) -> str:
        status, body = await http_get(self._host, self._port, "/json/version")
        if status != 200:
            raise WebSocketError(f"/json/version returned {status}")
        info = json.loads(body or b"{}")
        url = info.get("webSocketDebuggerUrl") or ""
        if "://" not in url:
            raise WebSocketError("browser endpoint has no webSocketDebuggerUrl")
        # ws://127.0.0.1:9223/devtools/browser/ABC -> /devtools/browser/ABC
        return "/" + url.split("://", 1)[1].split("/", 1)[1]

    async def _connect(self) -> DevToolsSocket:
        if self._socket is not None:
            return self._socket
        path = await self._page_path()
        self._socket = await DevToolsSocket.connect(self._host, self._port, path)
        return self._socket

    async def _connect_browser(self) -> DevToolsSocket:
        if self._browser is not None:
            return self._browser
        path = await self._browser_path()
        self._browser = await DevToolsSocket.connect(self._host, self._port, path)
        return self._browser

    async def _socket_for(self, method: str) -> DevToolsSocket:
        if method.startswith(BROWSER_DOMAINS):
            return await self._connect_browser()
        return await self._connect()

    async def call(self, method: str, params: dict | None = None) -> dict:
        async with self._lock:
            socket = await self._socket_for(method)
            self._ids += 1
            message_id = self._ids
            await socket.send_text(json.dumps({"id": message_id, "method": method, "params": params or {}}))
            while True:
                raw = await asyncio.wait_for(socket.recv_text(), timeout=self._timeout)
                try:
                    message = json.loads(raw)
                except ValueError:
                    continue
                if message.get("id") != message_id:
                    continue  # an event; the console does not subscribe to any
                if "error" in message:
                    error = message["error"] or {}
                    raise WebSocketError(str(error.get("message") or error))
                return message.get("result") or {}

    def drop(self) -> None:
        if self._socket is not None:
            self._socket.close()
            self._socket = None
        if self._browser is not None:
            self._browser.close()
            self._browser = None


# --------------------------------------------------------------------------- #
# HTTP surface
# --------------------------------------------------------------------------- #
async def handle(reader: asyncio.StreamReader, writer: asyncio.StreamWriter, devtools: DevTools) -> None:
    try:
        head = await asyncio.wait_for(reader.readuntil(b"\r\n\r\n"), timeout=10)
        lines = head.decode("latin-1").split("\r\n")
        method, path, _ = lines[0].split(" ", 2)
        headers = {}
        for line in lines[1:]:
            if ":" in line:
                key, _, value = line.partition(":")
                headers[key.strip().lower()] = value.strip()
        length = int(headers.get("content-length") or 0)
        body = await reader.readexactly(length) if length else b""

        if method == "GET" and path.split("?", 1)[0] in ("/json/version", "/json/list"):
            try:
                status, payload = await http_get(CHROMIUM_HOST, CHROMIUM_PORT, path.split("?", 1)[0])
            except (asyncio.TimeoutError, OSError, ValueError) as error:
                # Chromium not up yet, or it stopped answering: the readiness
                # probe should see a 502, not a closed connection.
                await _respond(
                    writer, 502, "application/json", json.dumps({"error": f"{error.__class__.__name__}"}).encode()
                )
                return
            await _respond(writer, status, "application/json", payload)
            return

        if method == "GET" and path.split("?", 1)[0] == "/healthz":
            await _respond(writer, 200, "application/json", json.dumps({"ok": True}).encode())
            return

        if method == "POST" and path.split("?", 1)[0] == "/cdp":
            try:
                payload = json.loads(body or b"{}")
            except ValueError:
                await _respond(writer, 400, "application/json", json.dumps({"error": "invalid JSON body"}).encode())
                return
            command = str(payload.get("method") or "")
            if not command:
                await _respond(writer, 400, "application/json", json.dumps({"error": "method is required"}).encode())
                return
            try:
                result = await devtools.call(command, payload.get("params") or {})
            except (WebSocketError, asyncio.TimeoutError, OSError) as error:
                # Drop the socket so the next request reconnects: Chromium
                # restarts, and a half-open socket would poison every later call.
                devtools.drop()
                await _respond(
                    writer, 502, "application/json", json.dumps({"error": f"{error.__class__.__name__}: {error}"}).encode()
                )
                return
            await _respond(writer, 200, "application/json", json.dumps({"result": result}).encode())
            return

        await _respond(writer, 404, "application/json", json.dumps({"error": "not found"}).encode())
    except (asyncio.IncompleteReadError, asyncio.LimitOverrunError, asyncio.TimeoutError, OSError, ValueError):
        pass
    finally:
        try:
            writer.close()
        except Exception:  # noqa: BLE001
            pass


async def _respond(writer: asyncio.StreamWriter, status: int, content_type: str, payload: bytes) -> None:
    reason = {200: "OK", 400: "Bad Request", 404: "Not Found", 502: "Bad Gateway"}.get(status, "OK")
    writer.write(
        (
            f"HTTP/1.1 {status} {reason}\r\n"
            f"Content-Type: {content_type}\r\n"
            f"Content-Length: {len(payload)}\r\n"
            "Connection: close\r\n"
            "\r\n"
        ).encode()
        + payload
    )
    await writer.drain()


async def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--listen-address", default="0.0.0.0")
    parser.add_argument("--listen-port", type=int, default=LISTEN_PORT)
    args = parser.parse_args()

    devtools = DevTools(CHROMIUM_HOST, CHROMIUM_PORT)
    server = await asyncio.start_server(
        lambda r, w: handle(r, w, devtools), host=args.listen_address, port=args.listen_port, backlog=64
    )
    print(
        f"cdp-bridge: http {args.listen_address}:{args.listen_port} -> "
        f"ws {CHROMIUM_HOST}:{CHROMIUM_PORT}",
        flush=True,
    )
    async with server:
        await server.serve_forever()
    return 0


if __name__ == "__main__":
    try:
        sys.exit(asyncio.run(main()))
    except KeyboardInterrupt:
        sys.exit(0)
