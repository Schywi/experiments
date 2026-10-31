"""sandbox-console: a read-only pane over live agent-sandbox Chromium sandboxes.

Milestones 1-3 of `research/rfc-sandbox-console.md`:

    M1  GET  /                                the grid of claimed sandboxes
    M2  GET  /sandboxes/{id}/shot             one PNG, through the router
        POST /sandboxes/{id}/click            a real click, then a new PNG
    M3  POST /sandboxes/{id}/reachability     live isolation evidence

There is no create, no delete and no `pods/exec`: the RBAC in
`chart/templates/rbac.yaml` grants `get/list/watch` only, and the isolation
badge is measured from inside the sandbox's own browser rather than by running
commands in the pod.
"""

from __future__ import annotations

import asyncio
import json
import re
import time
from contextlib import asynccontextmanager

from pathlib import Path
from urllib.parse import urlparse

from fastapi import FastAPI, HTTPException, Request, Response
from fastapi.responses import HTMLResponse, JSONResponse, RedirectResponse, StreamingResponse
from pydantic import BaseModel
from starlette.concurrency import run_in_threadpool

import asyncio

import audit
import cdp
import compare as compare_mode
import config
import inventory
import kube
import lifecycle
import probes
import records
import recipes
import render
import render_runs
import runs
import traffic
from concurrency import BoundedFIFO, QueueFull

LIMITER = BoundedFIFO(config.CDP_CONCURRENCY, config.CDP_QUEUE_SIZE)


class CreateRequest(BaseModel):
    """M4: what to create. `claim` inherits pool warmth; `sandbox` is bare.

    `url` points the new browser at a page. A claim adopts a pod that booted
    before the request, so the page cannot be part of its environment: the url is
    opened right after adoption, with the same navigate the edit box uses.
    """

    kind: str = "claim"
    url: str | None = None


class ClickRequest(BaseModel):
    """A click in *displayed* pixels plus the factor that maps it to the viewport."""

    x: float
    y: float
    scale: float = 1.0


class UrlRequest(BaseModel):
    """The page to open in one sandbox's browser."""

    url: str


class ScrollRequest(BaseModel):
    """A wheel delta in CSS pixels. Positive scrolls down."""

    dy: int


ALLOWED_URL_SCHEMES = ("http", "https")
MAX_URL_LENGTH = 2048
# One arrow press moves at most this many CSS pixels.
MAX_SCROLL = 2000


def clean_url(raw: str) -> str:
    """Validate a URL the pane is about to navigate a sandbox to.

    Chromium will happily open `file:///tmp/chrome-data/...`, which would let the
    input paint a sandbox's own disk onto the pane, so the scheme is checked
    rather than trusted. A bare host is accepted and read as `https://host`,
    which is what a person typing `example.com` means.
    """
    text = (raw or "").strip()
    if not text:
        raise HTTPException(status_code=400, detail="url is empty")
    if len(text) > MAX_URL_LENGTH:
        raise HTTPException(status_code=400, detail=f"url is longer than {MAX_URL_LENGTH} characters")
    # `data:text/html,...` and `javascript:...` have no `://`, so the prefix test
    # is on the scheme grammar rather than on the separator.
    has_scheme = re.match(r"^[a-zA-Z][a-zA-Z0-9+.\-]*:", text) is not None
    candidate = text if has_scheme else f"https://{text}"
    parsed = urlparse(candidate)
    scheme = (parsed.scheme or "").lower()
    if scheme not in ALLOWED_URL_SCHEMES:
        raise HTTPException(
            status_code=400,
            detail=f"only {', '.join(ALLOWED_URL_SCHEMES)} urls can be opened, not {scheme or 'this'}",
        )
    if not parsed.hostname:
        raise HTTPException(status_code=400, detail="url has no host")
    try:
        port = parsed.port
    except ValueError:
        raise HTTPException(status_code=400, detail="url has an invalid port") from None
    if port is not None and not 0 < port < 65536:
        raise HTTPException(status_code=400, detail="url has an invalid port")
    return candidate


class Screen:
    """Everything the console keeps in memory: last frames, probes, limits."""

    def __init__(self):
        self._shots: dict[str, tuple[float, bytes]] = {}
        self._reach: dict[str, dict] = {}
        self._notes: dict[str, str] = {}
        self._last_shot_source = "live"
        self._urls: dict[str, str] = {}
        self._locks: dict[str, asyncio.Lock] = {}
        # The browser context each sandbox's commands are addressed at. Empty
        # means the default page, which is what the Pool view has always driven.
        self._active_context: dict[str, str] = {}
        self.cdp = cdp.RouterCDP(
            config.ROUTER_URL,
            port=config.SANDBOX_PORT,
            namespace=config.SANDBOX_NAMESPACE,
            token=kube.router_token(),
            connect_timeout=config.CDP_CONNECT_TIMEOUT,
            command_timeout=config.CDP_COMMAND_TIMEOUT,
            viewport=(config.VIEWPORT_WIDTH, config.VIEWPORT_HEIGHT),
            transport=config.CDP_TRANSPORT,
        )

    # -- state -------------------------------------------------------------

    def lock(self, sandbox_id: str) -> asyncio.Lock:
        if sandbox_id not in self._locks:
            self._locks[sandbox_id] = asyncio.Lock()
        return self._locks[sandbox_id]

    def shot_ts(self, sandbox_id: str) -> int:
        cached = self._shots.get(sandbox_id)
        return int(cached[0]) if cached else 0

    def cached_shot(self, sandbox_id: str) -> tuple[float, bytes] | None:
        return self._shots.get(sandbox_id)

    def store_shot(self, sandbox_id: str, payload: bytes) -> None:
        self._shots[sandbox_id] = (time.time(), payload)
        self._notes.pop(sandbox_id, None)

    def note(self, sandbox_id: str, message: str) -> None:
        self._notes[sandbox_id] = message

    def url(self, sandbox_id: str) -> str | None:
        return self._urls.get(sandbox_id)

    def store_url(self, sandbox_id: str, url: str | None) -> None:
        if url:
            self._urls[sandbox_id] = url

    def reach_key(self, sandbox_id: str, pod_ip: str | None) -> str:
        return f"{sandbox_id}/{pod_ip or '-'}"

    def reach(self, sandbox_id: str, pod_ip: str | None) -> dict | None:
        return self._reach.get(self.reach_key(sandbox_id, pod_ip))

    def store_reach(self, sandbox_id: str, pod_ip: str | None, value: dict) -> None:
        self._reach[self.reach_key(sandbox_id, pod_ip)] = value
        self._notes.pop(sandbox_id, None)

    # -- collection --------------------------------------------------------

    async def create(self, kind: str) -> lifecycle.Created:
        """Create one sandbox: a claim from the pool, or a bare Sandbox."""
        if not config.WRITE_ENABLED:
            raise HTTPException(status_code=403, detail="write access is disabled on this pane")
        report = await self.report()
        live = report.running + report.pending
        if live >= config.MAX_CONCURRENT_SANDBOXES:
            raise HTTPException(
                status_code=409,
                detail=(
                    f"{live} sandboxes already live; the ceiling is "
                    f"{config.MAX_CONCURRENT_SANDBOXES} (experiment 02)"
                ),
            )
        loop = asyncio.get_running_loop()
        if kind == "sandbox":
            if not config.ALLOW_RAW_SANDBOX:
                raise HTTPException(
                    status_code=403,
                    detail="allowRawSandbox is off: a bare Sandbox has no NetworkPolicy, cluster DNS and a mounted token",
                )
            created = await loop.run_in_executor(
                None,
                lambda: lifecycle.create_sandbox(
                    config.SANDBOX_NAMESPACE,
                    image=config.SANDBOX_IMAGE,
                    target_url=config.TARGET_URL,
                    cdp_port=config.SANDBOX_PORT,
                ),
            )
        else:
            created = await loop.run_in_executor(
                None, lambda: lifecycle.create_claim(config.SANDBOX_NAMESPACE, config.WARM_POOL)
            )
        audit.record(
            config.AUDIT_LOG,
            "create",
            kind=created.kind,
            name=created.name,
            namespace=created.namespace,
        )
        # Time it in the background and write the numbers down: the four timers
        # only exist while the sandbox is being born, and the response cannot wait.
        task = asyncio.create_task(
            lifecycle.time_and_record(
                self.cdp,
                created,
                image=config.SANDBOX_IMAGE,
                target_url=config.TARGET_URL,
            )
        )
        _BACKGROUND.add(task)
        task.add_done_callback(_BACKGROUND.discard)
        return created

    async def delete(self, sandbox_id: str) -> str:
        if not config.WRITE_ENABLED:
            raise HTTPException(status_code=403, detail="write access is disabled on this pane")
        if not lifecycle.valid_name(sandbox_id):
            raise HTTPException(status_code=400, detail="invalid sandbox name")
        removed = await asyncio.get_running_loop().run_in_executor(
            None, lifecycle.delete_any, config.SANDBOX_NAMESPACE, sandbox_id
        )
        self._shots.pop(sandbox_id, None)
        self._urls.pop(sandbox_id, None)
        self._reach = {key: value for key, value in self._reach.items() if not key.startswith(f"{sandbox_id}/")}
        audit.record(config.AUDIT_LOG, "delete", removed=removed)
        return removed

    async def report(self) -> inventory.Report:
        snapshot = await run_in_threadpool(kube.snapshot, config.SANDBOX_NAMESPACE)
        report = inventory.build_report(
            snapshot.sandboxes,
            claims=snapshot.claims,
            pods=snapshot.pods,
            usage=snapshot.usage,
            max_tiles=config.MAX_TILES,
        )
        report.errors = snapshot.errors
        decorated = []
        for tile in report.tiles:
            tile = tile.replace(
                screenshot_ts=self.shot_ts(tile.id),
                reachability=self.reach(tile.id, tile.pod_ip),
                note=self._notes.get(tile.id),
                url=self._urls.get(tile.id),
            )
            decorated.append(tile)
        # How many sessions each browser is carrying. The grid is where an
        # operator decides which sandbox to open, so the count belongs on the
        # tile; it is read from the pod's own browser and cached, and a browser
        # that will not answer reports `None` rather than zero.
        counts = await _context_counts([tile.id for tile in decorated if tile.ready])
        report.tiles = [tile.replace(contexts=counts.get(tile.id)) for tile in decorated]
        return report

    async def tile_by_claim(self, claim_name: str) -> inventory.Tile | None:
        """The tile a claim adopted, once the controller has bound it."""
        report = await self.report()
        for tile in report.tiles:
            if tile.claim == claim_name:
                return tile
        return None

    async def wait_for_sandbox(self, created: lifecycle.Created) -> str | None:
        """The sandbox name a fresh create produced, or None if it never appeared.

        A bare Sandbox is named by the request. A claim is not: the controller
        binds it to a warm pod a moment later, and `status.sandbox.name` is the
        only place that answer exists. `report()` already joins the two.
        """
        if created.kind == "sandbox":
            return created.name
        deadline = time.monotonic() + config.CREATE_ADOPT_SECONDS
        while time.monotonic() < deadline:
            tile = await self.tile_by_claim(created.name)
            if tile is not None:
                return tile.id
            await asyncio.sleep(0.25)
        return None

    async def tile(self, sandbox_id: str) -> tuple[inventory.Tile | None, inventory.Report]:
        report = await self.report()
        for tile in report.tiles:
            if tile.id == sandbox_id:
                return tile, report
        return None, report

    # -- browser contexts --------------------------------------------------

    def active_context(self, sandbox_id: str) -> str:
        """The browser context this sandbox's commands are addressed at."""
        return self._active_context.get(sandbox_id, "")

    async def select_context(self, sandbox_id: str, context_id: str) -> dict:
        """Point this sandbox at one browser context, or back at the default page.

        Attaching is what makes a context's page drivable. `Target.attachToTarget
        {flatten: true}` returns a session that addresses Page and Input commands
        at that page only, so a click and a frame in one tab cannot land in
        another. An empty `context_id` returns to the default page.
        """
        targets = (await self.cdp.call(sandbox_id, "Target.getTargets") or {}).get("targetInfos") or []
        pages = [item for item in targets if isinstance(item, dict) and item.get("type") == "page"]
        if not context_id:
            self._active_context.pop(sandbox_id, None)
            self.cdp.use_context(sandbox_id, None)
            page = pages[0] if pages else {}
            return {
                "context": "",
                "target": str(page.get("targetId") or ""),
                "url": str(page.get("url") or ""),
            }
        page = next(
            (item for item in pages if str(item.get("browserContextId") or "") == context_id), None
        )
        if page is None:
            raise cdp.CDPError(f"no page in browser context {context_id}")
        attached = await self.cdp.call(
            sandbox_id,
            "Target.attachToTarget",
            {"targetId": page["targetId"], "flatten": True},
        )
        session_id = str((attached or {}).get("sessionId") or "")
        if not session_id:
            raise cdp.CDPError("attachToTarget returned no sessionId")
        self._active_context[sandbox_id] = context_id
        self.cdp.use_context(sandbox_id, session_id)
        # The stored frame belongs to the page that was on screen before.
        self._shots.pop(sandbox_id, None)
        return {
            "context": context_id,
            "target": str(page.get("targetId") or ""),
            "session": session_id,
            "url": str(page.get("url") or ""),
        }

    # -- actions -----------------------------------------------------------

    async def shot(self, sandbox_id: str, fresh: bool = False) -> bytes:
        """One frame. `fresh` skips the cache read, never the cache write.

        A cached frame is only trustworthy while nothing outside the pane
        changed the page. After a click or a navigation the caller asks for a
        capture regardless of age, so two viewers cannot show each other's
        stale picture. A selected browser context bypasses the read cache
        entirely: that frame belongs to one tab out of N, and serving it to
        another tab would be a lie about what is on screen.
        """
        in_context = self.cdp.context_session(sandbox_id) is not None
        cached = self.cached_shot(sandbox_id)
        if cached and not fresh and not in_context and (time.time() - cached[0]) < config.SHOT_TTL_SECONDS:
            return cached[1]
        async with self.lock(sandbox_id):
            cached = self.cached_shot(sandbox_id)
            if cached and not fresh and not in_context and (time.time() - cached[0]) < config.SHOT_TTL_SECONDS:
                return cached[1]
            payload = await self.cdp.screenshot(sandbox_id)
            self.store_shot(sandbox_id, payload)
            # The first frame is also the first chance to learn what the page
            # is: the template's TARGET_URL is the boot value, not the truth
            # after a navigation.
            if sandbox_id not in self._urls:
                try:
                    self.store_url(sandbox_id, await self.cdp.location(sandbox_id))
                except (cdp.CDPError, cdp.RouterError):
                    pass
            return payload

    async def click(self, sandbox_id: str, payload: ClickRequest) -> None:
        async with self.lock(sandbox_id):
            point = await self.cdp.click(
                sandbox_id,
                payload.x,
                payload.y,
                scale=payload.scale,
                width=config.VIEWPORT_WIDTH,
                height=config.VIEWPORT_HEIGHT,
            )
            # A click can navigate (a link, a form). Capture after the click's
            # own settle, so the stored frame is the one that shows the result.
            frame = await self.cdp.screenshot(sandbox_id)
            self.store_shot(sandbox_id, frame)
            try:
                self.store_url(sandbox_id, await self.cdp.location(sandbox_id))
            except (cdp.CDPError, cdp.RouterError):
                pass
        audit.record(
            config.AUDIT_LOG,
            "click",
            sandbox=sandbox_id,
            x=point[0],
            y=point[1],
            scale=payload.scale,
        )

    async def navigate(self, sandbox_id: str, url: str) -> str:
        """Open `url` in the sandbox's browser, then store the new frame.

        The page lives in the pod's Chromium, so this is a change to the running
        sandbox and it lasts until that pod goes away. A pool member boots at
        the template's TARGET_URL again.
        """
        async with self.lock(sandbox_id):
            landed = await self.cdp.navigate(sandbox_id, url)
            frame = await self.cdp.screenshot(sandbox_id)
            self.store_shot(sandbox_id, frame)
            self.store_url(sandbox_id, landed)
        audit.record(config.AUDIT_LOG, "navigate", sandbox=sandbox_id, url=landed)
        return landed

    async def scroll(self, sandbox_id: str, dy: int) -> None:
        """Scroll the sandbox's page one wheel notch and store the new frame."""
        async with self.lock(sandbox_id):
            await self.cdp.scroll(sandbox_id, dy)
            frame = await self.cdp.screenshot(sandbox_id)
            self.store_shot(sandbox_id, frame)
        audit.record(config.AUDIT_LOG, "scroll", sandbox=sandbox_id, dy=dy)

    async def probe(self, sandbox_id: str, tile: inventory.Tile) -> dict:
        spec_pod = None
        if tile.pod_name:
            spec_pod = await run_in_threadpool(kube.pod, config.SANDBOX_NAMESPACE, tile.pod_name)
        async with self.lock(sandbox_id):
            raw = await self.cdp.evaluate(
                sandbox_id, probes.build_expression(config.PROBE_TIMEOUT_MS)
            )
            observations = probes.parse(raw)
            reachability = probes.summarise(
                observations,
                config.PROBE_TIMEOUT_MS,
                probes.spec_claims(spec_pod),
            )
            self.store_reach(sandbox_id, tile.pod_ip, reachability)
        audit.record(
            config.AUDIT_LOG,
            "probe",
            sandbox=sandbox_id,
            pod_ip=tile.pod_ip,
            cluster=reachability.get("cluster"),
            metadata=reachability.get("metadata"),
            egress_public_ok=reachability.get("egress_public_ok"),
        )
        return reachability


def _http_error(error: Exception) -> HTTPException:
    """Map a transport failure to the status the pane shows."""
    if isinstance(error, cdp.RouterError):
        status = error.status or 502
        if status < 400:
            status = 502
        return HTTPException(status_code=status, detail=f"router: {error.detail}")
    if isinstance(error, cdp.CDPError):
        return HTTPException(status_code=502, detail=f"cdp: {error}")
    return HTTPException(status_code=500, detail=error.__class__.__name__)


SCREEN = Screen()
# Keep strong references to background tasks; a bare create_task can be collected
# before it runs (the assistant learned this the hard way).
_BACKGROUND: set = set()


@asynccontextmanager
async def lifespan(app: FastAPI):
    yield
    await SCREEN.cdp.close_all()


app = FastAPI(title="sandbox-console", version="0.1.0", lifespan=lifespan)

STATIC_DIR = Path(__file__).parent / "static"


@app.get("/static/{name}")
async def static_file(name: str) -> Response:
    """Serve the vendored front-end assets (currently just the UnoCSS runtime).

    Self-hosted rather than a CDN: the pane has to render on a LAN with no egress,
    and a node build step for Tailwind is not available in this image.
    """
    path = (STATIC_DIR / name).resolve()
    if STATIC_DIR.resolve() not in path.parents or not path.is_file():
        raise HTTPException(status_code=404, detail="not found")
    media = "text/javascript" if path.suffix == ".js" else "application/octet-stream"
    return Response(
        content=path.read_bytes(),
        media_type=media,
        headers={"Cache-Control": "public, max-age=86400"},
    )


@app.get("/", response_class=HTMLResponse)
async def index() -> HTMLResponse:
    """The one screen: counters, tiles, isolation badges, links out."""
    return HTMLResponse(render.page(await SCREEN.report()))


@app.get("/s/{sandbox_id}")
async def deep_link(sandbox_id: str, shot: int = 0) -> Response:
    """One sandbox as a page: the grid with that sandbox's dialog already open.

    This is the link form: it is a real document, so the browser's back button,
    a bookmark and an <iframe> all behave. `?shot=1` answers the frame instead of
    the page, for a client that only wants the pixels.
    """
    if shot:
        return RedirectResponse(
            url=f"/sandboxes/{sandbox_id}/shot?fresh=1", status_code=307
        )
    tile, report = await SCREEN.tile(sandbox_id)
    if tile is None:
        raise HTTPException(status_code=404, detail=f"no sandbox {sandbox_id}")
    return HTMLResponse(render.page(report, open_id=sandbox_id))


@app.get("/events")
async def events(request: Request) -> StreamingResponse:
    """Push the grid when it changes (Server-Sent Events).

    One connection per tab, no polling loop: the signature of the report is
    compared against the last one sent, so an idle cluster sends a heartbeat and
    nothing else. SSE rather than a WebSocket because the traffic is one-way and
    the router cannot carry an upgrade -- the DevTools socket lives inside the
    sandbox for the same reason.
    """
    pause = max(0.5, float(config.POLL_SECONDS))

    async def stream():
        last = None
        ticks = 0
        yield "retry: 3000\n\n"
        while True:
            if await request.is_disconnected():
                break
            try:
                report = await SCREEN.report()
                signature = render.signature(report)
                if signature != last:
                    last = signature
                    # SSE frames are line-based; collapse the fragment's newlines.
                    markup = render.grid(report).replace("\n", " ")
                    yield f"event: grid\ndata: {markup}\n\n"
                    ticks = 0
                else:
                    ticks += 1
                    if ticks >= 10:
                        yield "event: heartbeat\ndata: {}\n\n"
                        ticks = 0
            except Exception as error:  # noqa: BLE001 - a failing poll must not kill the stream
                yield f"event: heartbeat\ndata: {json.dumps({'error': str(error)[:120]})}\n\n"
            await asyncio.sleep(pause)

    return StreamingResponse(
        stream(),
        media_type="text/event-stream",
        headers={"Cache-Control": "no-cache", "Connection": "keep-alive", "X-Accel-Buffering": "no"},
    )


@app.get("/sandboxes", response_class=HTMLResponse)
async def sandboxes() -> HTMLResponse:
    """The polled fragment. Same markup htmx would swap, minus the htmx CDN."""
    return HTMLResponse(render.grid(await SCREEN.report()))


@app.get("/sandboxes/{sandbox_id}/shot")
async def shot(sandbox_id: str, fresh: int = 0) -> Response:
    """One PNG frame, captured through the router and cached for SHOT_TTL_SECONDS.

    `?fresh=1` forces a capture for the caller that just changed the page, so the
    frame it receives is the one after its own click rather than whatever the
    cache holds.
    """
    try:
        async with LIMITER.slot():
            payload = await SCREEN.shot(sandbox_id, fresh=bool(fresh))
    except QueueFull:
        raise HTTPException(
            status_code=429,
            detail="at capacity",
            headers={"Retry-After": str(config.RETRY_AFTER_SECONDS)},
        )
    except Exception as error:  # noqa: BLE001 - every transport failure is a tile state
        SCREEN.note(sandbox_id, f"shot failed: {error}")
        audit.record(config.AUDIT_LOG, "shot_failed", sandbox=sandbox_id, error=str(error))
        raise _http_error(error)
    return Response(
        content=payload,
        media_type="image/png",
        headers={
            "Cache-Control": "no-store" if fresh else f"public, max-age={int(config.SHOT_TTL_SECONDS)}",
            # No "which path served it" header here: when `fresh` is absent the
            # handler genuinely does not know whether Screen.shot read the cache,
            # and a header that guesses is worse than no header.
        },
    )


@app.post("/sandboxes/{sandbox_id}/click", response_class=HTMLResponse)
async def click(sandbox_id: str, payload: ClickRequest) -> HTMLResponse:
    """Dispatch a real mouse press/release, then return the refreshed tile."""
    tile, _ = await SCREEN.tile(sandbox_id)
    if tile is None:
        raise HTTPException(status_code=404, detail=f"no sandbox {sandbox_id}")
    try:
        async with LIMITER.slot():
            await SCREEN.click(sandbox_id, payload)
    except QueueFull:
        raise HTTPException(
            status_code=429,
            detail="at capacity",
            headers={"Retry-After": str(config.RETRY_AFTER_SECONDS)},
        )
    except Exception as error:  # noqa: BLE001
        SCREEN.note(sandbox_id, f"click failed: {error}")
        audit.record(config.AUDIT_LOG, "click_failed", sandbox=sandbox_id, error=str(error))
        raise _http_error(error)
    refreshed, _ = await SCREEN.tile(sandbox_id)
    return HTMLResponse(render.tile(refreshed or tile))


@app.post("/sandboxes/{sandbox_id}/url", response_class=HTMLResponse)
async def open_url(sandbox_id: str, payload: UrlRequest) -> HTMLResponse:
    """Open a url in one sandbox's browser and return the refreshed tile.

    The page lives in the pod's Chromium: this changes the running sandbox, not
    the template. A pool member that is deleted and re-claimed boots at the
    template's `TARGET_URL` again.
    """
    tile, _ = await SCREEN.tile(sandbox_id)
    if tile is None:
        raise HTTPException(status_code=404, detail=f"no sandbox {sandbox_id}")
    url = clean_url(payload.url)
    try:
        async with LIMITER.slot():
            await SCREEN.navigate(sandbox_id, url)
    except QueueFull:
        raise HTTPException(
            status_code=429,
            detail="at capacity",
            headers={"Retry-After": str(config.RETRY_AFTER_SECONDS)},
        )
    except Exception as error:  # noqa: BLE001
        SCREEN.note(sandbox_id, f"navigate failed: {error}")
        audit.record(config.AUDIT_LOG, "navigate_failed", sandbox=sandbox_id, url=url, error=str(error))
        raise _http_error(error)
    refreshed, _ = await SCREEN.tile(sandbox_id)
    return HTMLResponse(render.tile(refreshed or tile))


@app.post("/sandboxes/{sandbox_id}/scroll", response_class=HTMLResponse)
async def scroll(sandbox_id: str, payload: ScrollRequest) -> HTMLResponse:
    """Scroll one sandbox's page and return the refreshed tile.

    `dy` is clamped: one press moves at most MAX_SCROLL pixels, so a bad client
    cannot fling a page somewhere surprising.
    """
    tile, _ = await SCREEN.tile(sandbox_id)
    if tile is None:
        raise HTTPException(status_code=404, detail=f"no sandbox {sandbox_id}")
    dy = max(-MAX_SCROLL, min(MAX_SCROLL, payload.dy))
    if dy == 0:
        raise HTTPException(status_code=400, detail="dy is zero, nothing to scroll")
    try:
        async with LIMITER.slot():
            await SCREEN.scroll(sandbox_id, dy)
    except QueueFull:
        raise HTTPException(
            status_code=429,
            detail="at capacity",
            headers={"Retry-After": str(config.RETRY_AFTER_SECONDS)},
        )
    except Exception as error:  # noqa: BLE001
        SCREEN.note(sandbox_id, f"scroll failed: {error}")
        audit.record(config.AUDIT_LOG, "scroll_failed", sandbox=sandbox_id, dy=dy, error=str(error))
        raise _http_error(error)
    refreshed, _ = await SCREEN.tile(sandbox_id)
    return HTMLResponse(render.tile(refreshed or tile))


@app.post("/sandboxes/{sandbox_id}/reachability", response_class=HTMLResponse)
async def reachability(sandbox_id: str) -> HTMLResponse:
    """Live isolation evidence from inside the sandbox's own Chromium."""
    tile, _ = await SCREEN.tile(sandbox_id)
    if tile is None:
        raise HTTPException(status_code=404, detail=f"no sandbox {sandbox_id}")
    try:
        async with LIMITER.slot():
            await SCREEN.probe(sandbox_id, tile)
    except QueueFull:
        raise HTTPException(
            status_code=429,
            detail="at capacity",
            headers={"Retry-After": str(config.RETRY_AFTER_SECONDS)},
        )
    except Exception as error:  # noqa: BLE001
        SCREEN.note(sandbox_id, f"probe failed: {error}")
        audit.record(config.AUDIT_LOG, "probe_failed", sandbox=sandbox_id, error=str(error))
        raise _http_error(error)
    refreshed, _ = await SCREEN.tile(sandbox_id)
    return HTMLResponse(render.tile(refreshed or tile))


@app.get("/sandboxes/{sandbox_id}/expanded", response_class=HTMLResponse)
async def expanded(sandbox_id: str) -> HTMLResponse:
    """The modal: the same frame at a larger size, viewport unchanged."""
    tile, _ = await SCREEN.tile(sandbox_id)
    if tile is None:
        raise HTTPException(status_code=404, detail=f"no sandbox {sandbox_id}")
    return HTMLResponse(render.expanded(tile))


# --------------------------------------------------------------------------- #
# M4: create and delete from the pane
# --------------------------------------------------------------------------- #
class TrafficRequest(BaseModel):
    """How much synthetic traffic to start: N sessions, each on its own page.

    A session is a browser context inside the instance's Chromium, so `count` is
    the number of contexts created and the number of slots the wall will light.
    `urls` cycles over the sites a session should open; blank uses the built-in
    list, which is deliberately eight different origins.
    """

    count: int = 4
    urls: list[str] | None = None


def _no_capacity() -> HTTPException:
    return HTTPException(
        status_code=429,
        detail="at capacity",
        headers={"Retry-After": str(config.RETRY_AFTER_SECONDS)},
    )


class ActiveRequest(BaseModel):
    """Which browser context the dialog's frame and input are addressed at.

    Empty means the default page: the one the sandbox booted with, which is what
    the Pool view has always shown.
    """

    context: str = ""


@app.post("/sandboxes/{sandbox_id}/active")
async def select_active(sandbox_id: str, payload: ActiveRequest) -> JSONResponse:
    """Switch this sandbox's frame and input to one context, or the default page."""
    tile = await _known_tile(sandbox_id)
    try:
        async with LIMITER.slot():
            chosen = await SCREEN.select_context(tile.id, payload.context.strip())
    except QueueFull:
        raise _no_capacity()
    except Exception as error:  # noqa: BLE001 - a context that went away is a result
        raise _http_error(error)
    audit.record(
        config.AUDIT_LOG, "select_context", sandbox=tile.id, context=chosen["context"]
    )
    return JSONResponse({"sandbox": tile.id, **chosen})


@app.get("/sandboxes/{sandbox_id}/contexts")
async def list_contexts(sandbox_id: str) -> JSONResponse:
    """Every browser context in one instance, with the pages inside it.

    Reads only: `Target.getBrowserContexts` and `Target.getTargets`, which the
    in-pod bridge sends on the browser socket because Chromium refuses them on a
    page socket with "Not allowed". `active_context_id` is the tab the dialog is
    currently driving.
    """
    tile = await _known_tile(sandbox_id)
    try:
        async with LIMITER.slot():
            rows = await traffic.listing(SCREEN.cdp, tile.id)
    except QueueFull:
        raise _no_capacity()
    except traffic.TrafficError as error:
        raise HTTPException(status_code=502, detail=str(error))
    return JSONResponse(
        {
            "sandbox": tile.id,
            "contexts": rows,
            "sites": list(traffic.DEFAULT_SITES),
            "active_context_id": SCREEN.active_context(tile.id),
        }
    )


@app.post("/sandboxes/{sandbox_id}/contexts")
async def start_contexts(sandbox_id: str, payload: TrafficRequest) -> JSONResponse:
    """Start traffic: create `count` contexts, each opened at its own site."""
    if not config.WRITE_ENABLED:
        raise HTTPException(status_code=403, detail="write access is disabled on this pane")
    tile = await _known_tile(sandbox_id)
    try:
        async with LIMITER.slot():
            created = await traffic.start(SCREEN.cdp, tile.id, payload.count, payload.urls)
    except QueueFull:
        raise _no_capacity()
    except traffic.TrafficError as error:
        audit.record(config.AUDIT_LOG, "traffic_failed", sandbox=tile.id, error=str(error))
        raise HTTPException(status_code=502, detail=str(error))
    audit.record(
        config.AUDIT_LOG,
        "traffic_start",
        sandbox=tile.id,
        count=len(created),
        urls=[session.url for session in created],
    )
    return JSONResponse(
        status_code=202,
        content={
            "sandbox": tile.id,
            "created": [
                {
                    "browser_context_id": session.context_id,
                    "url": session.url,
                    "target_id": session.target_id,
                }
                for session in created
            ],
            "limit": config.MAX_SESSIONS_PER_BROWSER,
        },
    )


@app.delete("/sandboxes/{sandbox_id}/contexts")
async def stop_contexts(sandbox_id: str, all: int = 0) -> JSONResponse:
    """Dispose the contexts this console created, or every non-default context."""
    if not config.WRITE_ENABLED:
        raise HTTPException(status_code=403, detail="write access is disabled on this pane")
    tile = await _known_tile(sandbox_id)
    try:
        async with LIMITER.slot():
            result = await traffic.stop(SCREEN.cdp, tile.id, all_contexts=bool(all))
    except QueueFull:
        raise _no_capacity()
    except Exception as error:  # noqa: BLE001 - a browser that refuses is the result
        audit.record(config.AUDIT_LOG, "traffic_stop_failed", sandbox=tile.id, error=str(error))
        raise HTTPException(status_code=502, detail=str(error))
    audit.record(
        config.AUDIT_LOG,
        "traffic_stop",
        sandbox=tile.id,
        disposed=len(result["disposed"]),
        all=bool(all),
    )
    return JSONResponse({"sandbox": tile.id, **result})


@app.post("/sandboxes")
async def create_sandbox(payload: CreateRequest) -> JSONResponse:
    """Create a Claim (default) or a bare Sandbox, optionally on a page.

    The url is validated before anything is created: a refused url leaves no
    sandbox behind. When adoption does not finish inside CREATE_ADOPT_SECONDS the
    sandbox still exists, so the answer says so rather than failing the create.
    """
    url = clean_url(payload.url) if payload.url else None
    created = await SCREEN.create((payload.kind or "claim").lower())
    content: dict = {
        "kind": created.kind,
        "name": created.name,
        "namespace": created.namespace,
    }
    if url:
        sandbox_id = await SCREEN.wait_for_sandbox(created)
        if sandbox_id is None:
            content.update(
                {
                    "sandbox": None,
                    "url": None,
                    "note": (
                        f"created; not adopted within {config.CREATE_ADOPT_SECONDS:.0f}s, "
                        "navigate it once it appears"
                    ),
                }
            )
        else:
            try:
                await _run_action(sandbox_id, "navigate", SCREEN.navigate(sandbox_id, url), url=url)
                tile = await SCREEN.tile(sandbox_id)
                content.update({"sandbox": sandbox_id, "url": (tile[0].url if tile[0] else url)})
            except HTTPException as error:
                content.update(
                    {"sandbox": sandbox_id, "url": None, "navigate_error": str(error.detail)}
                )
    return JSONResponse(status_code=202, content=content)


@app.delete("/sandboxes/{sandbox_id}")
async def delete_sandbox(sandbox_id: str) -> JSONResponse:
    """Delete the Claim that owns this sandbox, or the Sandbox itself."""
    removed = await SCREEN.delete(sandbox_id)
    return JSONResponse(status_code=202, content={"removed": removed})


# --------------------------------------------------------------------------- #
# M5: recipes, streamed as newline-delimited JSON
# --------------------------------------------------------------------------- #
@app.get("/recipes")
async def list_recipes() -> dict:
    return {
        "running": recipes.busy_with(),
        "recipes": [
            {"name": item.name, "summary": item.summary, "blast_radius": item.blast_radius}
            for item in recipes.RECIPES.values()
        ],
    }


@app.post("/recipes/{name}")
async def run_recipe(name: str, count: int | None = None, hold: float = 0.0) -> StreamingResponse:
    """Run one bounded recipe. One at a time, and refused rather than queued."""
    if not config.WRITE_ENABLED and name != "capacity-check":
        raise HTTPException(status_code=403, detail="write access is disabled on this pane")
    # Checked before the stream is created: `recipes.stream` is an async
    # generator, so nothing inside it runs until the first line is read -- by
    # which point the response is already committed as 200 OK.
    if name not in recipes.RECIPES:
        raise HTTPException(status_code=404, detail=f"no recipe {name}")
    running = recipes.busy_with()
    if running:
        raise HTTPException(status_code=409, detail=f"recipe {running} is already running")
    stream = recipes.stream(name, count=count, hold=hold)

    async def body():
        try:
            async for item in stream:
                yield (json.dumps(item) + "\n").encode()
        except (recipes.RecipeBusy, recipes.UnknownRecipe) as error:
            yield (json.dumps({"line": f"refused: {error}", "done": True}) + "\n").encode()
        finally:
            audit.record(config.AUDIT_LOG, "recipe", recipe=name, count=count)

    return StreamingResponse(body(), media_type="application/x-ndjson")


# --------------------------------------------------------------------------- #
# M6: compare mode
# --------------------------------------------------------------------------- #
@app.post("/compare")
async def start_compare() -> JSONResponse:
    """Fire one warm Claim and (if allowed) one bare Sandbox, then time both."""
    if not config.WRITE_ENABLED:
        raise HTTPException(status_code=403, detail="write access is disabled on this pane")
    try:
        run = await compare_mode.run_compare(
            SCREEN.cdp,
            allow_raw=config.ALLOW_RAW_SANDBOX,
            timeout=config.CREATE_TIMEOUT_SECONDS,
        )
    except HTTPException:
        raise
    except Exception as error:  # noqa: BLE001 - a failed run is a reportable state
        raise HTTPException(status_code=502, detail=f"compare failed: {error}")
    audit.record(
        config.AUDIT_LOG,
        "compare",
        arms=[
            {"label": arm.label, "timers": arm.timers, "error": arm.error}
            for arm in run.arms
        ],
    )
    return JSONResponse(status_code=200, content=run.to_dict())


@app.post("/sandboxes/{sandbox_id}/compare", response_class=HTMLResponse)
async def compare_against(sandbox_id: str) -> HTMLResponse:
    """Compare one open sandbox against a fresh bare Sandbox, for the dialog.

    The left column reads its timings from the database (they only exist at
    creation), the right column times a bare Sandbox made for this run.
    """
    if not config.WRITE_ENABLED:
        raise HTTPException(status_code=403, detail="write access is disabled on this pane")
    if not lifecycle.valid_name(sandbox_id):
        raise HTTPException(status_code=400, detail="invalid sandbox name")
    try:
        run = await compare_mode.run_against(
            SCREEN.cdp,
            sandbox_id,
            allow_raw=config.ALLOW_RAW_SANDBOX,
            timeout=config.CREATE_TIMEOUT_SECONDS,
        )
    except Exception as error:  # noqa: BLE001
        raise HTTPException(status_code=502, detail=f"compare failed: {error}")
    audit.record(
        config.AUDIT_LOG,
        "compare_subject",
        sandbox=sandbox_id,
        arms=[{"label": arm.label, "timers": arm.timers} for arm in run.arms],
    )
    return HTMLResponse(render.compare_columns(run, allow_raw=config.ALLOW_RAW_SANDBOX, subject=sandbox_id))


@app.get("/compare", response_class=HTMLResponse)
async def compare_view() -> HTMLResponse:
    """The split view: the last run, plus p50 of the last three on hover."""
    latest = compare_mode.latest_run()
    return HTMLResponse(
        render.compare_page(
            latest,
            compare_mode.medians_from_db(),
            allow_raw=config.ALLOW_RAW_SANDBOX,
            write_enabled=config.WRITE_ENABLED,
        )
    )


# --------------------------------------------------------------------------- #
# M7: the demo run
# --------------------------------------------------------------------------- #
@app.post("/demo")
async def demo_fill() -> JSONResponse:
    """Fill the grid to `DEMO_TILES` by claiming from the pool."""
    if not config.WRITE_ENABLED:
        raise HTTPException(status_code=403, detail="write access is disabled on this pane")
    report = await SCREEN.report()
    live = report.running + report.pending
    wanted = max(0, min(config.DEMO_TILES, config.MAX_CONCURRENT_SANDBOXES) - live)
    created = []
    loop = asyncio.get_running_loop()
    for _ in range(wanted):
        try:
            item = await loop.run_in_executor(
                None, lambda: lifecycle.create_claim(config.SANDBOX_NAMESPACE, config.WARM_POOL)
            )
        except Exception as error:  # noqa: BLE001
            created.append({"error": str(error)})
            continue
        created.append({"name": item.name})
    audit.record(config.AUDIT_LOG, "demo_fill", created=len(created), target=config.DEMO_TILES)
    return JSONResponse(status_code=202, content={"created": created, "target": config.DEMO_TILES})


@app.post("/demo/teardown")
async def demo_teardown() -> JSONResponse:
    """Delete every claim this pane created, leaving the pool at its baseline."""
    if not config.WRITE_ENABLED:
        raise HTTPException(status_code=403, detail="write access is disabled on this pane")
    loop = asyncio.get_running_loop()
    claims = await loop.run_in_executor(None, lifecycle.managed_claims, config.SANDBOX_NAMESPACE)
    # Bare Sandboxes are not owned by a claim, so they need their own sweep;
    # without it the compare column of the demo would survive every teardown.
    sandboxes = await loop.run_in_executor(None, lifecycle.managed_sandboxes, config.SANDBOX_NAMESPACE)
    removed, failed = [], []
    for item in claims:
        name = (item.get("metadata") or {}).get("name")
        if not name:
            continue
        try:
            await loop.run_in_executor(None, lifecycle.delete_claim, config.SANDBOX_NAMESPACE, name)
            removed.append(name)
        except Exception as error:  # noqa: BLE001
            failed.append({"name": name, "error": str(error)})
    for item in sandboxes:
        name = (item.get("metadata") or {}).get("name")
        if not name:
            continue
        try:
            await loop.run_in_executor(None, lifecycle.delete_sandbox, config.SANDBOX_NAMESPACE, name)
            removed.append(name)
        except Exception as error:  # noqa: BLE001
            failed.append({"name": name, "error": str(error)})
    audit.record(config.AUDIT_LOG, "demo_teardown", removed=removed, failed=failed)
    return JSONResponse(status_code=200, content={"removed": removed, "failed": failed})


# --------------------------------------------------------------------------- #
# The run wall: session-lab runs, drawn as slots
#
# Additive to every route above it. The wall reads a run database read-only, so
# nothing here can write to a run that is still executing. See
# research/session-lifecycle-elasticity/16-session-wall-ui.md.
# --------------------------------------------------------------------------- #
def _run_cut(run: runs.Run, at: int) -> int:
    """The instant the wall draws: the one asked for, inside the run's window."""
    start, end = run.window
    if at and start <= at <= end:
        return at
    return run.default_cut()


async def _live_tiles() -> dict:
    """Instance name -> live tile, for the badges on an instance group.

    A wall renders whether or not the cluster answers: an unreachable API server
    is a fact about the console, and the run's own rows are still readable.
    """
    try:
        report = await SCREEN.report()
    except Exception:  # noqa: BLE001 - the wall renders without live state
        return {}
    tiles: dict = {}
    for tile in report.tiles:
        tiles[tile.id] = tile
        if tile.claim:
            tiles.setdefault(tile.claim, tile)
    return tiles


# `Target.getBrowserContexts` answers are reused per sandbox: the grid polls every
# POLL_SECONDS, and one call per tile per poll would spend the CDP limiter's
# budget on a badge. A refusal is remembered longer (REFUSED_TTL) so a pod whose
# bridge cannot answer is not re-probed on every poll.
_CONTEXT_COUNT_CACHE: dict[str, tuple[float, int | None, float]] = {}
_CONTEXT_COUNT_REFUSED_SECONDS = 20.0


async def _context_counts(names: list[str]) -> dict[str, int | None]:
    """Sessions per sandbox, read from each pod's own browser.

    The count is `Target.getBrowserContexts`, which lists non-default contexts —
    exactly the sessions. A browser whose bridge predates browser contexts
    answers `Not allowed`, and that is recorded as `None`: the tile then says
    "unknown" instead of claiming the sandbox is idle.

    Every call is bounded by a short timeout and they run concurrently, because
    this is on the grid's render path: one unreachable browser must cost a
    bounded delay, not the sum of the other tiles' timeouts.
    """
    now = time.time()
    found: dict[str, int | None] = {}
    pending: list[str] = []
    for name in names:
        cached = _CONTEXT_COUNT_CACHE.get(name)
        if cached is not None and now - cached[0] < cached[2]:
            found[name] = cached[1]
        else:
            pending.append(name)
    if not pending:
        return found

    async def one(name: str) -> tuple[str, int | None]:
        try:
            async with LIMITER.slot():
                answer = await asyncio.wait_for(
                    SCREEN.cdp.call(name, "Target.getBrowserContexts"),
                    timeout=config.CDP_CONNECT_TIMEOUT + 1.0,
                )
            return name, len([item for item in (answer or {}).get("browserContextIds") or []])
        except Exception:  # noqa: BLE001 - a refusal or a timeout is drawn as unknown
            return name, None

    for name, count in await asyncio.gather(*(one(name) for name in pending)):
        ttl = config.SLOT_TARGETS_TTL if count is not None else _CONTEXT_COUNT_REFUSED_SECONDS
        _CONTEXT_COUNT_CACHE[name] = (time.time(), count, ttl)
        found[name] = count
    return found


# `Target.getTargets` answers are reused for a few seconds per instance: the wall
# refreshes every RUN_REFRESH_SECONDS, and a 40-instance run at the CDP limiter's
# width would otherwise spend its whole budget on discovery.
_TARGETS_CACHE: dict[str, tuple[float, list[dict]]] = {}


async def _live_contexts(instances: list[str]) -> dict[str, list[dict]]:
    """Page targets per instance, read from each pod's own browser.

    A target carries a `browserContextId`, so this is the only call needed to know
    how many slots an instance is really serving right now. Verified against a
    live sandbox: one page target, `browserContextId` present, 43 ms through the
    in-pod bridge.
    """
    now = time.time()
    found: dict[str, list[dict]] = {}
    for name in instances:
        cached = _TARGETS_CACHE.get(name)
        if cached is not None and now - cached[0] < config.SLOT_TARGETS_TTL:
            found[name] = cached[1]
            continue
        try:
            async with LIMITER.slot():
                result = await SCREEN.cdp.call(name, "Target.getTargets")
        except Exception:  # noqa: BLE001 - a browser that does not answer is drawn, not raised
            continue
        pages = [
            item
            for item in (result.get("targetInfos") or [])
            if isinstance(item, dict) and item.get("type") == "page"
        ]
        pages.sort(key=lambda item: str(item.get("browserContextId") or ""))
        _TARGETS_CACHE[name] = (now, pages)
        found[name] = pages
    return found


async def _known_run(run_id: str) -> runs.Run:
    try:
        return await run_in_threadpool(runs.load, config.RUNS_DIR, run_id)
    except runs.RunNotFound:
        raise HTTPException(status_code=404, detail=f"no run {run_id}")


@app.get("/runs", response_class=HTMLResponse)
async def runs_index() -> HTMLResponse:
    """Every run database the console can see, newest first."""
    refs = await run_in_threadpool(runs.discover, config.RUNS_DIR)
    return HTMLResponse(render_runs.index(refs))


@app.get("/runs/{run_id}", response_class=HTMLResponse)
async def run_view(run_id: str, at: int = 0, instance: str = "", live: int = 1) -> Response:
    """One run: the rack, or one instance's tracks when `instance` is given."""
    run = await _known_run(run_id)
    cut = _run_cut(run, at)
    tiles = await _live_tiles() if live else {}
    contexts = await _live_contexts(list(tiles)) if live else {}
    return HTMLResponse(
        render_runs.run_page(run, cut, instance=instance, tiles=tiles, contexts=contexts)
    )


@app.get("/runs/{run_id}/wall", response_class=HTMLResponse)
async def run_wall_fragment(run_id: str, at: int = 0) -> Response:
    """The wall alone, for the live refresh. No cluster call on this path."""
    run = await _known_run(run_id)
    return HTMLResponse(render_runs.wall(run, _run_cut(run, at)))


@app.get("/api/runs")
async def api_runs() -> JSONResponse:
    """The runs index as JSON, for a caller that is not a browser."""
    refs = await run_in_threadpool(runs.discover, config.RUNS_DIR)
    return JSONResponse([ref.to_dict() for ref in refs])


@app.get("/api/runs/{run_id}")
async def api_run(run_id: str, at: int = 0) -> JSONResponse:
    """One run's counts, buckets and slots as JSON."""
    run = await _known_run(run_id)
    return JSONResponse(runs.to_json(run, _run_cut(run, at)))


@app.get("/healthz")
async def healthz(request: Request) -> dict:
    """Configuration echo plus the last observed tile count. No secrets."""
    report = await SCREEN.report()
    return {
        "ok": True,
        "namespace": config.SANDBOX_NAMESPACE,
        "router": config.ROUTER_URL,
        "sandbox_port": config.SANDBOX_PORT,
        "viewport": [config.VIEWPORT_WIDTH, config.VIEWPORT_HEIGHT],
        "max_tiles": config.MAX_TILES,
        "tiles": len(report.tiles),
        "pool_idle": report.pool_idle,
        "pool_ready": report.pool_ready,
        "admitting": LIMITER.admitted,
        "errors": report.errors,
        "durable_timings": records.durable(),
    }


# --------------------------------------------------------------------------- #
# Machine interface: the same actions, JSON instead of card fragments
#
# The pane answers a person with HTML fragments. An assistant's tools are not a
# browser, so these routes return the tile projection, the frame URL and the
# outcome of the action. Same limiter, same audit log, same transport: there is
# one implementation of routing, readiness and frame freshness.
# --------------------------------------------------------------------------- #
def _tile_payload(tile: inventory.Tile) -> dict:
    """A tile as JSON, plus the URL that fetches its frame."""
    payload = tile.to_dict()
    payload["shot"] = f"/sandboxes/{tile.id}/shot?v={tile.screenshot_ts}"
    return payload


async def _known_tile(sandbox_id: str) -> inventory.Tile:
    tile, _ = await SCREEN.tile(sandbox_id)
    if tile is None:
        raise HTTPException(status_code=404, detail=f"no sandbox {sandbox_id}")
    return tile


async def _run_action(sandbox_id: str, action: str, work, **fields) -> None:
    """Run one CDP action under the limiter, mapping failures like the pane does."""
    try:
        async with LIMITER.slot():
            await work
    except QueueFull:
        raise HTTPException(
            status_code=429,
            detail="at capacity",
            headers={"Retry-After": str(config.RETRY_AFTER_SECONDS)},
        )
    except Exception as error:  # noqa: BLE001 - a transport failure is a tool result
        SCREEN.note(sandbox_id, f"{action} failed: {error}")
        audit.record(config.AUDIT_LOG, f"{action}_failed", sandbox=sandbox_id, error=str(error), **fields)
        raise _http_error(error)


@app.get("/api/sandboxes")
async def api_sandboxes() -> JSONResponse:
    """Every claimed sandbox the pane would draw, with the counters behind it."""
    report = await SCREEN.report()
    return JSONResponse(
        content={
            "ok": True,
            "namespace": config.SANDBOX_NAMESPACE,
            "tiles": [_tile_payload(tile) for tile in report.tiles],
            "running": report.running,
            "pending": report.pending,
            "pool_ready": report.pool_ready,
            "pool_idle": report.pool_idle,
            "errors": list(report.errors or []),
            "max_tiles": config.MAX_TILES,
        }
    )


@app.get("/api/sandboxes/{sandbox_id}")
async def api_sandbox(sandbox_id: str) -> JSONResponse:
    """One tile. Poll this after an action instead of re-listing everything."""
    return JSONResponse(content={"ok": True, "tile": _tile_payload(await _known_tile(sandbox_id))})


@app.post("/api/sandboxes/{sandbox_id}/url")
async def api_open_url(sandbox_id: str, payload: UrlRequest) -> JSONResponse:
    """Open a url in the sandbox and return where it landed."""
    await _known_tile(sandbox_id)
    url = clean_url(payload.url)
    await _run_action(sandbox_id, "navigate", SCREEN.navigate(sandbox_id, url), url=url)
    tile = await _known_tile(sandbox_id)
    return JSONResponse(content={"ok": True, "url": tile.url or url, "tile": _tile_payload(tile)})


@app.post("/api/sandboxes/{sandbox_id}/click")
async def api_click(sandbox_id: str, payload: ClickRequest) -> JSONResponse:
    """Dispatch a real mouse press/release at a viewport coordinate."""
    await _known_tile(sandbox_id)
    await _run_action(sandbox_id, "click", SCREEN.click(sandbox_id, payload), scale=payload.scale)
    tile = await _known_tile(sandbox_id)
    return JSONResponse(content={"ok": True, "tile": _tile_payload(tile)})


@app.post("/api/sandboxes/{sandbox_id}/scroll")
async def api_scroll(sandbox_id: str, payload: ScrollRequest) -> JSONResponse:
    """Scroll the page one wheel notch; `dy` positive scrolls down."""
    await _known_tile(sandbox_id)
    dy = max(-MAX_SCROLL, min(MAX_SCROLL, payload.dy))
    if dy == 0:
        raise HTTPException(status_code=400, detail="dy is zero, nothing to scroll")
    await _run_action(sandbox_id, "scroll", SCREEN.scroll(sandbox_id, dy), dy=dy)
    tile = await _known_tile(sandbox_id)
    return JSONResponse(content={"ok": True, "dy": dy, "tile": _tile_payload(tile)})


@app.post("/api/sandboxes/{sandbox_id}/reachability")
async def api_reachability(sandbox_id: str) -> JSONResponse:
    """The isolation evidence, measured from inside the sandbox's own browser."""
    tile = await _known_tile(sandbox_id)
    evidence: dict = {}

    async def probe() -> None:
        evidence.update(await SCREEN.probe(sandbox_id, tile))

    await _run_action(sandbox_id, "probe", probe())
    refreshed = await _known_tile(sandbox_id)
    return JSONResponse(content={"ok": True, "reachability": evidence, "tile": _tile_payload(refreshed)})
