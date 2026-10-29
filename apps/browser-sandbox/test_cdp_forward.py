"""The forwarder is the only reason the router can reach Chromium.

Chromium 130 ignores `--remote-debugging-address` and binds DevTools to loopback;
the kubelet readiness probe and the sandbox-router both dial the pod IP. This
test proves the proxy actually moves bytes in both directions, which is the
property the tile depends on.
"""

import asyncio
import socket
import subprocess
import sys
import time
from pathlib import Path

HERE = Path(__file__).parent
FORWARDER = HERE / "cdp-forward.py"


def _free_port() -> int:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def test_forwarder_relays_bytes_both_ways():
    echo_port = _free_port()
    listen_port = _free_port()

    async def echo_server():
        async def handle(reader, writer):
            while True:
                chunk = await reader.read(4096)
                if not chunk:
                    break
                writer.write(b"echo:" + chunk)
                await writer.drain()
            writer.close()

        return await asyncio.start_server(handle, "127.0.0.1", echo_port)

    async def roundtrip():
        server = await echo_server()
        process = subprocess.Popen(
            [
                sys.executable,
                str(FORWARDER),
                "--listen-address", "127.0.0.1",
                "--listen-port", str(listen_port),
                "--upstream-host", "127.0.0.1",
                "--upstream-port", str(echo_port),
            ],
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            text=True,
        )
        try:
            # Wait for the listener to accept, then check one request/reply pair.
            reply = b""
            for _ in range(50):
                try:
                    reader, writer = await asyncio.open_connection("127.0.0.1", listen_port)
                except OSError:
                    await asyncio.sleep(0.1)
                    continue
                writer.write(b"GET /json/version HTTP/1.1\r\n\r\n")
                await writer.drain()
                reply = await asyncio.wait_for(reader.read(4096), timeout=5)
                writer.close()
                break
            return reply
        finally:
            process.terminate()
            process.wait(timeout=5)
            server.close()
            await server.wait_closed()

    reply = asyncio.run(roundtrip())
    assert reply == b"echo:GET /json/version HTTP/1.1\r\n\r\n"


def test_forwarder_closes_the_connection_when_chromium_is_absent():
    listen_port = _free_port()
    dead_port = _free_port()  # nothing listens here

    async def connect_through():
        process = subprocess.Popen(
            [
                sys.executable,
                str(FORWARDER),
                "--listen-address", "127.0.0.1",
                "--listen-port", str(listen_port),
                "--upstream-host", "127.0.0.1",
                "--upstream-port", str(dead_port),
            ],
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            text=True,
        )
        try:
            for _ in range(50):
                try:
                    reader, writer = await asyncio.open_connection("127.0.0.1", listen_port)
                except OSError:
                    await asyncio.sleep(0.1)
                    continue
                # The proxy closes without a response, so the readiness probe
                # sees an empty read rather than hanging.
                return await asyncio.wait_for(reader.read(64), timeout=5)
            raise AssertionError("forwarder never accepted a connection")
        finally:
            process.terminate()
            process.wait(timeout=5)

    assert asyncio.run(connect_through()) == b""


def test_entrypoint_uses_the_loopback_port_upstream():
    script = (HERE / "entrypoint.sh").read_text()
    # The published port and Chromium's real port must stay distinct: pointing
    # the forwarder at its own listen port would loop.
    assert 'CDP_PORT="${CDP_PORT:-9222}"' in script
    assert 'CDP_UPSTREAM_PORT="${CDP_UPSTREAM_PORT:-9223}"' in script
    assert '--remote-debugging-port="${CDP_UPSTREAM_PORT}"' in script
    # The address flag is gone from the command line: it is documented in the
    # header as ignored, so match the assignment form only.
    assert "--remote-debugging-address=" not in script
    assert "cdp-forward" in script
    assert "trap cleanup TERM INT" in script
