#!/usr/bin/env python3
"""Publish Chromium's loopback-only DevTools endpoint on the pod IP.

Chromium 130 accepts `--remote-debugging-address=0.0.0.0` and binds the DevTools
HTTP server to `127.0.0.1` anyway. Verified on this cluster against the built
image: `curl 127.0.0.1:9222/json/version` answers, `curl <podIP>:9222` is refused
from inside the pod's own network namespace, and `--headless=old` behaves the
same way. That makes the endpoint unreachable for the two callers that matter:
the kubelet readiness probe and the sandbox-router, both of which dial the pod IP.

So the browser keeps its loopback listener and this process re-publishes it:

    router / kubelet -> podIP:9222 -> here -> 127.0.0.1:9223 -> Chromium

A raw TCP proxy, not an HTTP one: the DevTools client speaks WebSocket upgrades
on the same port, and rewriting headers would break the `Host` handling Chromium
relies on (an IP literal is accepted; anything else is rejected).

No dependency beyond the interpreter the Playwright image already carries.
"""

import argparse
import asyncio
import sys


async def _pump(reader: asyncio.StreamReader, writer: asyncio.StreamWriter) -> None:
    try:
        while True:
            chunk = await reader.read(65536)
            if not chunk:
                break
            writer.write(chunk)
            await writer.drain()
    except (ConnectionResetError, BrokenPipeError, asyncio.IncompleteReadError):
        pass
    finally:
        try:
            writer.close()
        except Exception:  # noqa: BLE001 - closing twice is not an error
            pass


async def _handle(client_reader, client_writer, upstream_host: str, upstream_port: int) -> None:
    try:
        upstream_reader, upstream_writer = await asyncio.wait_for(
            asyncio.open_connection(upstream_host, upstream_port), timeout=5
        )
    except (OSError, asyncio.TimeoutError):
        # Chromium is not listening yet. Closing without a response makes the
        # readiness probe fail, which is exactly what should happen.
        client_writer.close()
        return
    await asyncio.gather(
        _pump(client_reader, upstream_writer),
        _pump(upstream_reader, client_writer),
    )


async def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--listen-address", default="0.0.0.0")
    parser.add_argument("--listen-port", type=int, default=9222)
    parser.add_argument("--upstream-host", default="127.0.0.1")
    parser.add_argument("--upstream-port", type=int, default=9223)
    args = parser.parse_args()

    server = await asyncio.start_server(
        lambda r, w: _handle(r, w, args.upstream_host, args.upstream_port),
        host=args.listen_address,
        port=args.listen_port,
        # DevTools frames carry base64 PNGs; the default limit is not a problem
        # for a byte pump, but 64 KiB reads keep large screenshots moving.
        backlog=64,
    )
    print(
        f"cdp-forward: {args.listen_address}:{args.listen_port} -> "
        f"{args.upstream_host}:{args.upstream_port}",
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
