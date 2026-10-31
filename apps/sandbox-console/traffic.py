"""Synthetic sessions: browser contexts inside one Chromium, each on its own page.

A session in this repository is a slot, `(instance, browserContextId)`
(`research/session-lifecycle-elasticity/15-capacity-plane-and-demand-plane.md` §3).
The console can now mint those slots directly, which is what makes the wall
testable without waiting for a session-lab run: create N contexts on one
instance, point each at a different site, and the rack draws N lit slots.

Three facts decide the implementation:

1. `Target.createBrowserContext` and `Target.getBrowserContexts` are refused on a
   page socket with "Not allowed" (measured on `claim-mvvlyivc`, Chrome 130). The
   in-pod bridge routes `Target.*` to the browser socket
   (`apps/browser-sandbox/cdp-bridge.py`, `BROWSER_DOMAINS`), so the console only
   has to send the command.
2. `Target.createTarget {url, browserContextId}` opens the page at that URL, so a
   context is populated in one call — no per-target `Page.navigate` and no
   `sessionId` is needed to start traffic.
3. A context created this way is not the runner's. The console keeps its own
   ledger per instance so `stop` disposes what it made and leaves whatever
   session-lab created alone.
"""

from __future__ import annotations

import asyncio
import time
from dataclasses import dataclass, field
from urllib.parse import urlparse

import config

# Pages a session is pointed at when the operator does not name any. Distinct
# origins matter: same-site sessions share caches and connections, which is the
# thing a per-session context is supposed to isolate.
DEFAULT_SITES = (
    "https://example.com/",
    "https://example.org/",
    "https://example.net/",
    "https://www.iana.org/help/example-domains",
    "https://developer.mozilla.org/en-US/",
    "https://www.w3.org/",
    "https://httpbin.org/html",
    "https://en.wikipedia.org/wiki/Chromium_(web_browser)",
)


class TrafficError(Exception):
    """The browser refused a command, so no context was created."""


class InvalidURL(TrafficError):
    """A site was not an absolute http(s) URL, so nothing was created."""


def normalise_url(raw: str) -> str:
    """An absolute http(s) URL, or `InvalidURL`.

    Chromium accepts a schemeless host in `Target.createTarget` **without
    erroring** and gives the new tab `about:blank`, so `google.com` silently
    became an empty session. Everything is made absolute here, before a context
    exists to leak.
    """
    value = str(raw or "").strip()
    if not value:
        raise InvalidURL("empty site")
    parsed = urlparse(value)
    # A scheme that is not http(s) is refused before anything is prepended:
    # `javascript:` and `about:` are not sites, and `ftp://host` would otherwise
    # be turned into `https://ftp://host`.
    if parsed.scheme and parsed.scheme not in ("http", "https"):
        raise InvalidURL(f"not an http(s) url: {raw!r}")
    if not parsed.scheme:
        value = "https://" + value
        parsed = urlparse(value)
    if not parsed.netloc:
        raise InvalidURL(f"not an http(s) url: {raw!r}")
    return value


@dataclass
class Session:
    """One context the console created inside one instance."""

    sandbox: str
    context_id: str
    url: str
    target_id: str = ""
    created_at: float = field(default_factory=time.time)
    # Set when the page did not land where it was sent. Such a context is
    # disposed before `start` returns, so a blank tab is never left behind.
    error: str = ""


@dataclass
class Ledger:
    """What this console created, per instance.

    In-process, like the screenshot cache: the console runs one uvicorn worker
    with `replicas: 1`, and a context that outlives a console restart is the
    operator's to clean up (the wall still shows it, from the browser).
    """

    by_sandbox: dict[str, dict[str, Session]] = field(default_factory=dict)

    def add(self, session: Session) -> None:
        self.by_sandbox.setdefault(session.sandbox, {})[session.context_id] = session

    def ours(self, sandbox: str) -> dict[str, Session]:
        return dict(self.by_sandbox.get(sandbox, {}))

    def forget(self, sandbox: str, context_id: str) -> None:
        self.by_sandbox.get(sandbox, {}).pop(context_id, None)

    def drop(self, sandbox: str) -> None:
        self.by_sandbox.pop(sandbox, None)


LEDGER = Ledger()


def _limit() -> int:
    return max(1, config.MAX_SESSIONS_PER_BROWSER)


async def start(
    client,
    sandbox: str,
    count: int,
    urls: list[str] | None = None,
    ledger: Ledger | None = None,
) -> tuple[list[Session], list[Session]]:
    """Create `count` contexts on one instance, each opened at its own URL.

    Returns `(created, rejected)`. A rejected session is one whose page did not
    land where it was sent: the context is disposed before this returns, so a
    tab showing `about:blank` is never left behind to be mistaken for a session.
    """
    ledger = ledger if ledger is not None else LEDGER
    provided = [str(url).strip() for url in (urls or []) if str(url).strip()]
    # Normalising raises before any context exists: `google.com` is sent to
    # Chromium as `https://google.com`, because the browser accepts the bare host
    # and quietly gives the tab about:blank.
    choices = [normalise_url(url) for url in provided] if provided else list(DEFAULT_SITES)
    wanted = max(1, min(int(count), _limit()))
    # Continue through the site list rather than restarting it: starting traffic
    # twice should give two sets of distinct origins, not two copies of the same.
    offset = len(ledger.ours(sandbox))
    created: list[Session] = []
    for index in range(wanted):
        url = choices[(offset + index) % len(choices)]
        try:
            answer = await client.call(sandbox, "Target.createBrowserContext")
        except Exception as error:  # noqa: BLE001 - the browser's refusal is the result
            raise TrafficError(f"createBrowserContext: {error}") from error
        context_id = str((answer or {}).get("browserContextId") or "")
        if not context_id:
            raise TrafficError("createBrowserContext returned no browserContextId")
        session = Session(sandbox=sandbox, context_id=context_id, url=url)
        try:
            target = await client.call(
                sandbox,
                "Target.createTarget",
                {"url": url, "browserContextId": context_id},
            )
            session.target_id = str((target or {}).get("targetId") or "")
        except Exception as error:  # noqa: BLE001 - the context exists; the page did not
            session.error = f"createTarget: {error}"
        created.append(session)
    return await _verify(client, sandbox, created, ledger)


async def _verify(
    client, sandbox: str, created: list[Session], ledger: Ledger
) -> tuple[list[Session], list[Session]]:
    """Keep only the sessions whose page actually loaded where it was sent.

    `Target.createTarget` reports success for a URL Chromium cannot use, so the
    only trustworthy answer is the target's own URL, read back after the
    navigation has had a moment. A context that is still blank is disposed: a
    session the operator cannot see is worse than a failed start, because it
    looks like traffic.
    """
    if not created:
        return [], []
    landed: dict[str, str] = {}
    for attempt in range(3):
        try:
            answer = await client.call(sandbox, "Target.getTargets")
        except Exception:  # noqa: BLE001 - no reading means take the create at its word
            answer = {}
        landed = {
            str(item.get("targetId") or ""): str(item.get("url") or "")
            for item in (answer or {}).get("targetInfos") or []
            if isinstance(item, dict) and item.get("type") == "page"
        }
        if all(
            session.target_id in landed and landed[session.target_id] != "about:blank"
            for session in created
            if not session.error
        ):
            break
        if attempt < 2:
            await asyncio.sleep(0.2 * (attempt + 1))
    kept: list[Session] = []
    rejected: list[Session] = []
    for session in created:
        if session.error:
            rejected.append(session)
            continue
        page_url = landed.get(session.target_id, "")
        if page_url == "about:blank" or not page_url:
            session.error = f"the page opened at {page_url or 'nothing'}"
            rejected.append(session)
            continue
        kept.append(session)
    for session in kept:
        ledger.add(session)
    for session in rejected:
        ledger.forget(sandbox, session.context_id)
        try:
            await client.call(
                sandbox, "Target.disposeBrowserContext", {"browserContextId": session.context_id}
            )
        except Exception:  # noqa: BLE001 - a context already gone is disposed enough
            pass
    return kept, rejected


async def listing(client, sandbox: str, ledger: Ledger | None = None) -> list[dict]:
    """Every context the browser reports, with the pages inside it.

    `Target.getBrowserContexts` lists the contexts; `Target.getTargets` says which
    page lives in each. The default context appears in the second and not the
    first, which is how it is told apart from a session.
    """
    ledger = ledger if ledger is not None else LEDGER
    try:
        contexts_answer = await client.call(sandbox, "Target.getBrowserContexts")
    except Exception as error:  # noqa: BLE001 - pre-browser-socket bridges answer "Not allowed"
        raise TrafficError(f"getBrowserContexts: {error}") from error
    ids = [str(item) for item in (contexts_answer or {}).get("browserContextIds") or []]
    try:
        targets = (await client.call(sandbox, "Target.getTargets") or {}).get("targetInfos") or []
    except Exception:  # noqa: BLE001 - a context with no page is still a context
        targets = []
    pages: dict[str, list[dict]] = {}
    for target in targets:
        if not isinstance(target, dict) or target.get("type") != "page":
            continue
        pages.setdefault(str(target.get("browserContextId") or ""), []).append(target)
    known = ledger.ours(sandbox)
    rows = []
    for context_id in ids:
        mine = known.get(context_id)
        rows.append(
            {
                "browser_context_id": context_id,
                "urls": [str(page.get("url") or "") for page in pages.get(context_id, [])],
                "targets": [str(page.get("targetId") or "") for page in pages.get(context_id, [])],
                "created_by_console": mine is not None,
                "age_s": int(time.time() - mine.created_at) if mine else None,
            }
        )
    return rows


async def stop(
    client,
    sandbox: str,
    *,
    all_contexts: bool = False,
    ledger: Ledger | None = None,
) -> dict:
    """Dispose the contexts this console created, or every non-default context."""
    ledger = ledger if ledger is not None else LEDGER
    if all_contexts:
        answer = await client.call(sandbox, "Target.getBrowserContexts")
        targets = [str(item) for item in (answer or {}).get("browserContextIds") or []]
    else:
        targets = list(ledger.ours(sandbox))
    disposed: list[str] = []
    failed: list[dict] = []
    for context_id in targets:
        try:
            await client.call(sandbox, "Target.disposeBrowserContext", {"browserContextId": context_id})
        except Exception as error:  # noqa: BLE001 - a context already gone is a success
            failed.append({"browser_context_id": context_id, "error": str(error)})
            continue
        ledger.forget(sandbox, context_id)
        disposed.append(context_id)
    return {"disposed": disposed, "failed": failed}
