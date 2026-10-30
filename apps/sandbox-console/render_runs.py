"""The run wall: what a session-lab run looks like on screen.

`runs.py` answers what happened; this module places it. Four zooms of one object,
from `research/session-lifecycle-elasticity/16-session-wall-ui.md`:

    skyline    every slot in arrival order, one cell each        (the run's shape)
    wall       bucket → instance → slot glyphs                   (the rack)
    tracks     one instance's slots on a shared clock            (the multiplicity)
    card       one slot's life: bars, counters, identity         (the detail)

The HTML is built by string concatenation with `render.esc` for every value, the
same way `render.py` builds the pool view, and for the same reason: the console
ships one file per page and has no template engine.
"""

from __future__ import annotations

from datetime import datetime

import config
import render
import runs

# One glyph pair per slot position. The vocabulary is the note's §4.2 table.
GLYPHS = {
    runs.HOLDING: "██",
    runs.WAITING: "░░",
    runs.HOLE: "⊗ ",
    runs.DONE: "▪ ",
    runs.FAILED: "##",
    runs.FUTURE: "· ",
}
FREE = "··"

GLYPH_LEGEND = (
    (runs.HOLDING, "██", "holding a slot"),
    (runs.WAITING, "░░", "admitted, no slot yet"),
    (runs.HOLE, "⊗ ", "never got an instance"),
    (runs.DONE, "▪ ", "ended, slot kept as a stub"),
    (runs.FAILED, "##", "failed"),
    ("free", FREE, "free slot: the browser can take another"),
)


def _clock(nanoseconds: int) -> str:
    if not nanoseconds:
        return "—"
    return datetime.fromtimestamp(nanoseconds / 1_000_000_000).strftime("%H:%M:%S")


def _stamp(nanoseconds: int) -> str:
    if not nanoseconds:
        return "—"
    return datetime.fromtimestamp(nanoseconds / 1_000_000_000).strftime("%Y-%m-%d %H:%M:%S")


def _duration(nanoseconds: int) -> str:
    """A span as `850ms`, `30.1s`, `4m 12s` or `1h 03m`."""
    if not nanoseconds:
        return "0ms"
    seconds = nanoseconds / 1_000_000_000
    if seconds < 1:
        return f"{int(nanoseconds / 1_000_000)}ms"
    if seconds < 60:
        return f"{seconds:.1f}s"
    if seconds < 3600:
        return f"{int(seconds // 60)}m {int(seconds % 60):02d}s"
    return f"{int(seconds // 3600)}h {int((seconds % 3600) // 60):02d}m"


def _ms(value: float) -> str:
    if not value:
        return "—"
    if value < 1000:
        return f"{value:.0f}ms"
    return f"{value / 1000:.1f}s"


def _glyph(state: str) -> str:
    return GLYPHS.get(state, FREE)


# --------------------------------------------------------------------------- #
# Fragments
# --------------------------------------------------------------------------- #
def skyline(cells: list[dict], sessions: int, cut_ns: int, window: tuple[int, int]) -> str:
    """The pinned strip: the run's shape before any numeral is read."""
    if not cells:
        return '<div class="skyline empty-strip"><span class="muted">no sessions recorded</span></div>'
    bars = "".join(
        f'<span class="sky s-{render.esc(cell["state"])}" '
        f'title="{render.esc(cell["state"])} · sessions {cell["first_id"]}–{cell["last_id"]} '
        f'· {render.esc(cell["bucket"])}"></span>'
        for cell in cells
    )
    start, end = window
    total = max(1, sessions)
    marks = "".join(
        f'<span style="left:{fraction}%">{render.esc(label)}</span>'
        for fraction, label in (
            (0, "0"),
            (25, str(int(total * 0.25))),
            (50, str(int(total * 0.50))),
            (75, str(int(total * 0.75))),
            (100, str(total)),
        )
    )
    return (
        '<figure class="skyline-figure">'
        f'<div class="skyline" role="img" aria-label="every slot in arrival order, {sessions} sessions">'
        f"{bars}</div>"
        f'<div class="skyline-ruler">{marks}</div>'
        f'<figcaption class="skyline-caption muted">slot order is arrival order · '
        f'window {_clock(start)} → {_clock(end)} · drawn at {_clock(cut_ns)}</figcaption>'
        "</figure>"
    )


def legend() -> str:
    items = "".join(
        f'<span class="legend-item"><b class="s-{render.esc(state)}">{render.esc(glyph)}</b>'
        f"<span>{render.esc(label)}</span></span>"
        for state, glyph, label in GLYPH_LEGEND
    )
    return f'<div class="legend">{items}</div>'


def _slot_strip(group: runs.InstanceGroup, cut_ns: int) -> str:
    """One row of glyphs: one position per slot the browser has room for."""
    cells = []
    for position in range(max(group.capacity, len(group.slots), 1)):
        slot = group.slots[position] if position < len(group.slots) else None
        if slot is None:
            cells.append(
                f'<span class="slot free" title="free slot {position}: no session assigned">{FREE}</span>'
            )
            continue
        held = slot.holding_at(cut_ns)
        if held is not None:
            state = runs.HOLDING
            title = f"slot {position} · session {held.id} · {_duration(_hold_of(held, cut_ns))} held"
        else:
            last = slot.last()
            state = last.state_at(cut_ns) if last is not None else runs.FUTURE
            title = (
                f"slot {position} · last session {last.id} · {last.outcome or 'pending'}"
                if last is not None
                else f"slot {position} · never used"
            )
        cells.append(
            f'<span class="slot s-{render.esc(state)}" title="{render.esc(title)}">'
            f"{_glyph(state)}</span>"
        )
    return "".join(cells)


def _hold_of(session: runs.Session, cut_ns: int) -> int:
    start, end = session.hold_window
    return max(0, min(cut_ns, end if end > start else cut_ns) - start)


def group_card(group: runs.InstanceGroup, cut_ns: int, window: tuple[int, int], live_tile) -> str:
    holding = group.holding_at(cut_ns)
    capacity = max(group.capacity, len(group.slots), 1)
    queue = runs.percentile([item.queue_ns / 1_000_000 for item in group.sessions], 0.5)
    state = "busy" if holding else "idle"
    live = ""
    if live_tile is not None:
        live = (
            f'<span class="badge badge-outline" title="live kubernetes state">'
            f"{render.esc(live_tile.phase)}</span>"
            f'<span class="muted mono">{render.esc(live_tile.pod_ip or "no ip")}</span>'
        )
    exact = (
        '<span class="badge badge-success" title="slot ids came from the run database">exact</span>'
        if group.exact
        else '<span class="badge badge-warning" title="slots inferred by packing hold windows">by time</span>'
    )
    href = f"?at={cut_ns}&amp;instance={render.esc(group.name)}"
    return (
        f'<article class="group {state}" id="group-{render.esc(group.name)}">'
        f'<header><a class="group-name mono" href="{href}" title="open this instance\'s tracks">'
        f"{render.esc(group.name)}</a>{live}{exact}</header>"
        f'<div class="slots">{_slot_strip(group, cut_ns)}</div>'
        f'<footer class="group-foot">'
        f'<span class="mono">{holding}/{capacity}</span><span class="muted">lit</span>'
        f'<span class="spacer"></span>'
        f'<span class="muted">q {_ms(queue)}</span>'
        f'<span class="muted">{len(group.sessions)} ses</span>'
        "</footer>"
        "</article>"
    )


def bucket_section(bucket: runs.Bucket, cut_ns: int, window: tuple[int, int], tiles: dict) -> str:
    groups = "".join(
        group_card(group, cut_ns, window, tiles.get(group.name)) for group in bucket.instances
    )
    if not groups:
        groups = (
            '<div class="empty"><strong>No instance in this bucket.</strong>'
            f"<span>{bucket.unassigned} session(s) never received an instance, so there is no slot "
            "to draw.</span></div>"
        )
    unassigned = (
        f'<span class="badge badge-warning" title="sessions that never received an instance">'
        f"{bucket.unassigned} unassigned</span>"
        if bucket.unassigned
        else ""
    )
    return (
        f'<section class="bucket"><h2>{render.esc(bucket.name)}</h2>'
        f'<div class="bucket-meta">'
        f"<span>{len(bucket.sessions)} sessions</span>"
        f"<span>{len(bucket.instances)} instances</span>"
        f"<span>hold p50 {_ms(bucket.hold_p50_ms)}</span>"
        f"<span>queue p50 {_ms(bucket.queue_p50_ms)}</span>"
        f"{unassigned}</div>"
        f'<div class="groups">{groups}</div></section>'
    )


def tracks(group: runs.InstanceGroup, cut_ns: int, window: tuple[int, int]) -> str:
    """One instance's slots as horizontal tracks on a shared clock."""
    start, end = window
    span = max(1, end - start)
    rows = []
    for slot in group.slots:
        bars = []
        for session in slot.sessions:
            session_start, session_end = session.hold_window
            left = (session_start - start) / span * 100
            width = max(0.4, (session_end - session_start) / span * 100)
            state = session.state_at(cut_ns)
            label = f"{session.id} · {session.outcome or 'pending'} · {_duration(session_end - session_start)}"
            bars.append(
                f'<span class="bar s-{render.esc(state)}" style="left:{left:.3f}%;width:{width:.3f}%" '
                f'title="{render.esc(label)}"></span>'
            )
        markers = "".join(
            f'<span class="mark" style="left:{(session.arrival_ns - start) / span * 100:.3f}%" '
            f'title="arrival of session {session.id}"></span>'
            for session in slot.sessions
            if session.arrival_ns
        )
        held = slot.holding_at(cut_ns)
        rows.append(
            f'<div class="track"><span class="track-name mono">{render.esc(slot.key)}</span>'
            f'<div class="track-line">{markers}{"".join(bars)}'
            f'<span class="now" style="left:{(cut_ns - start) / span * 100:.3f}%"></span></div>'
            f'<span class="track-state mono s-{"holding" if held else "idle"}">'
            f'{"session " + str(held.id) if held else "free"}</span></div>'
        )
    free = max(0, group.capacity - len(group.slots))
    if free:
        rows.append(
            f'<div class="track"><span class="track-name mono">—</span>'
            f'<div class="track-line"><span class="muted">{free} free position(s)</span></div>'
            '<span class="track-state mono idle">free</span></div>'
        )
    return (
        f'<section class="tracks"><h2>{render.esc(group.name)}</h2>'
        f'<div class="bucket-meta"><span>{len(group.slots)} slot(s)</span>'
        f"<span>{group.capacity} position(s)</span>"
        f'<span>{_clock(start)} → {_clock(end)}</span></div>{"".join(rows)}</section>'
    )


def stats(run: runs.Run, cut_ns: int) -> str:
    counts = run.counts(cut_ns)
    items = [
        ("planned", str(run.planned)),
        ("stored", str(run.stored)),
        ("holding", str(counts[runs.HOLDING])),
        ("waiting", str(counts[runs.WAITING])),
        ("holes", str(counts[runs.HOLE])),
        ("failed", str(counts[runs.FAILED])),
        ("peak", str(run.peak_concurrency)),
        ("at", _clock(cut_ns)),
    ]
    cells = "".join(
        f'<div class="stat" data-stat="{render.esc(label)}">'
        f'<span class="stat-label">{render.esc(label)}</span>'
        f'<span class="stat-value">{render.esc(value)}</span></div>'
        for label, value in items
    )
    return f'<div class="stats">{cells}</div>'


def scrub(run: runs.Run, cut_ns: int, focus: str) -> str:
    """Step the drawn instant, or snap it to a landmark."""
    start, end = run.window
    step = max(1, (end - start) // 20)
    target = f"&amp;instance={render.esc(focus)}" if focus else ""
    links = [
        ("◀", max(start, cut_ns - step), "earlier"),
        ("peak", run.peak_ns, "the instant the run held the most slots"),
        ("now", run.read_at_ns if run.live else end, "the newest observed transition"),
        ("end", end, "the run's last transition"),
        ("▶", min(end, cut_ns + step), "later"),
    ]
    buttons = "".join(
        f'<a class="btn btn-sm {"btn-primary" if value == cut_ns else "btn-outline"}" '
        f'href="?at={value}{target}" title="{render.esc(title)}">{render.esc(label)}</a>'
        for label, value, title in links
    )
    return (
        f'<div class="scrub"><span class="muted">draw at</span>{buttons}'
        f'<span class="mono muted">{_stamp(cut_ns)}</span>'
        f'<span class="spacer"></span>'
        f'<span class="muted">{_duration(cut_ns - start)} into a {_duration(end - start)} window</span></div>'
    )


def wall(run: runs.Run, cut_ns: int, tiles: dict | None = None) -> str:
    """Everything the wall owns: counts, skyline, scrubber, buckets or tracks."""
    tiles = tiles or {}
    window = run.window
    focus = ""
    body = "".join(bucket_section(bucket, cut_ns, window, tiles) for bucket in run.buckets)
    if not run.sessions:
        body = (
            '<div class="empty"><strong>This run has no sessions.</strong>'
            "<span>The database exists and holds a run row, but no session reached it.</span></div>"
        )
    error = f'<p class="note">read error: {render.esc(run.error)}</p>' if run.error else ""
    truncated = (
        f'<p class="hint">{run.truncated} session(s) not shown '
        f"(RUN_MAX_SESSIONS={config.RUN_MAX_SESSIONS})</p>"
        if run.truncated
        else ""
    )
    return (
        f'<div id="wall-inner" data-cut="{cut_ns}">'
        f"{stats(run, cut_ns)}"
        f"{skyline(runs.skyline(run.sessions, cut_ns, config.RUN_MAX_SKYLINE), run.stored, cut_ns, window)}"
        f"{scrub(run, cut_ns, focus)}"
        f"{legend()}{error}{body}{truncated}"
        "</div>"
    )


def tracks_page(run: runs.Run, cut_ns: int, instance: str) -> str:
    """The focus view for one instance, plus every other instance as a link."""
    window = run.window
    found = None
    for bucket in run.buckets:
        for group in bucket.instances:
            if group.name == instance:
                found = group
                break
        if found is not None:
            break
    links = "".join(
        f'<a class="btn btn-sm {"btn-primary" if group.name == instance else "btn-outline"}" '
        f'href="?at={cut_ns}&amp;instance={render.esc(group.name)}">{render.esc(group.name)}</a>'
        for bucket in run.buckets
        for group in bucket.instances
    )
    if found is None:
        body = (
            f'<div class="empty"><strong>No instance named {render.esc(instance)}.</strong>'
            "<span>It may have been cleaned up, or it belonged to a different run.</span></div>"
        )
    else:
        body = tracks(found, cut_ns, window)
    return (
        f'<div id="wall-inner" data-cut="{cut_ns}">{stats(run, cut_ns)}'
        f"{scrub(run, cut_ns, instance)}"
        f'<div class="focus-links"><a class="btn btn-sm btn-ghost" href="?at={cut_ns}">'
        f"← back to the rack</a>{links}</div>"
        f"{legend()}{body}</div>"
    )


# --------------------------------------------------------------------------- #
# Pages
# --------------------------------------------------------------------------- #
RUN_CSS = """
.run-title { display: flex; align-items: baseline; gap: var(--space-3); flex-wrap: wrap; margin-bottom: var(--space-3); }
.run-title h2 { font-size: var(--text-xl); }
.skyline-figure { margin: 0 0 var(--space-4); }
.skyline { display: flex; gap: 1px; height: 26px; align-items: stretch; }
.skyline .sky { flex: 1 1 0; min-width: 1px; border-radius: 1px; }
.skyline-ruler { position: relative; height: 16px; margin-top: 2px; }
.skyline-ruler span { position: absolute; transform: translateX(-50%);
  font-size: 10px; color: hsl(var(--muted-foreground)); font-variant-numeric: tabular-nums; }
.skyline-ruler span:first-child { transform: none; }
.skyline-caption { font-size: var(--text-xs); }
.skyline.empty-strip { align-items: center; justify-content: center; }
.scrub { display: flex; align-items: center; gap: var(--space-2); flex-wrap: wrap;
  margin: var(--space-3) 0; }
.legend { display: flex; flex-wrap: wrap; gap: var(--space-3); margin: var(--space-2) 0 var(--space-4); }
.legend-item { display: inline-flex; align-items: center; gap: 6px; font-size: var(--text-xs);
  color: hsl(var(--muted-foreground)); }
.legend-item b { font-family: var(--font-mono); font-size: var(--text-sm); }
.bucket { margin-bottom: var(--space-6); }
.bucket h2 { font-size: var(--text-lg); }
.bucket-meta { display: flex; gap: var(--space-3); flex-wrap: wrap; font-size: var(--text-xs);
  color: hsl(var(--muted-foreground)); margin: var(--space-1) 0 var(--space-3); }
.groups { display: grid; gap: var(--space-2); grid-template-columns: repeat(auto-fill, minmax(240px, 1fr)); }
.group { border: 1px solid hsl(var(--border)); border-radius: var(--radius);
  background: hsl(var(--card)); padding: var(--space-2) var(--space-3); min-width: 0; }
.group.busy { border-color: hsl(var(--primary) / .45); }
.group header { display: flex; align-items: center; gap: 6px; min-width: 0; }
.group-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: var(--text-sm); }
.group header .badge { padding: 0 6px; font-size: 10px; }
.slots { display: flex; gap: 4px; flex-wrap: wrap; margin: var(--space-2) 0; }
.slots .slot { font-family: var(--font-mono); font-size: var(--text-base); line-height: 1; }
.slots .slot.free { opacity: .45; }
.group-foot { display: flex; align-items: center; gap: var(--space-2); font-size: 11px;
  color: hsl(var(--muted-foreground)); }
.tracks { display: flex; flex-direction: column; gap: 6px; }
.track { display: grid; grid-template-columns: 64px 1fr 78px; gap: var(--space-2); align-items: center; }
.track-name { font-size: 11px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  color: hsl(var(--muted-foreground)); }
.track-line { position: relative; height: 18px; background: hsl(var(--muted) / .5);
  border-radius: 4px; overflow: hidden; }
.track-line .bar { position: absolute; top: 2px; bottom: 2px; border-radius: 2px; opacity: .9; }
.track-line .mark { position: absolute; top: 0; bottom: 0; width: 1px;
  background: hsl(var(--foreground) / .18); }
.track-line .now { position: absolute; top: -2px; bottom: -2px; width: 2px;
  background: hsl(var(--warning)); }
.track-state { font-size: 11px; }
.focus-links { display: flex; gap: var(--space-1); flex-wrap: wrap; margin: var(--space-3) 0; }
.s-holding { color: hsl(var(--primary)); background: hsl(var(--primary) / .55); }
.sky.s-holding, .slot.s-holding { color: hsl(var(--primary)); }
.s-waiting { color: hsl(var(--warning)); background: hsl(var(--warning) / .45); }
.sky.s-waiting, .slot.s-waiting { color: hsl(var(--warning)); }
.s-hole { color: hsl(var(--muted-foreground)); background: hsl(240 4% 28%); }
.sky.s-hole, .slot.s-hole { color: hsl(240 5% 45%); }
.s-done { color: hsl(var(--success)); background: hsl(var(--success) / .5); }
.sky.s-done, .slot.s-done { color: hsl(var(--success)); }
.s-failed { color: hsl(var(--destructive)); background: hsl(var(--destructive) / .75); }
.sky.s-failed, .slot.s-failed { color: hsl(var(--destructive)); }
.s-future, .s-free { color: hsl(var(--muted-foreground)); background: hsl(240 4% 18%); }
.sky.s-future, .slot.s-future { color: hsl(240 5% 40%); }
.run-row { display: grid; grid-template-columns: 160px repeat(5, minmax(64px, 1fr)) 1fr;
  gap: var(--space-2); align-items: center; padding: var(--space-2) 0;
  border-bottom: 1px solid hsl(var(--border)); font-size: var(--text-sm); }
.run-row.head { color: hsl(var(--muted-foreground)); font-size: var(--text-xs);
  text-transform: uppercase; letter-spacing: .04em; }
.run-row .muted { font-size: var(--text-xs); }
"""

RUN_JS = """
(function () {
  const wall = document.getElementById('wall');
  if (!wall || wall.dataset.live !== '1') return;
  const id = wall.dataset.run;
  const cut = wall.dataset.cut;
  const every = parseInt(wall.dataset.refresh || '5', 10) * 1000;
  let stopped = false;
  async function refresh() {
    if (stopped || document.hidden) return;
    try {
      const response = await fetch('/runs/' + encodeURIComponent(id) + '/wall?at=' + encodeURIComponent(cut),
                                  {headers: {'accept': 'text/html'}});
      if (!response.ok) throw new Error('status ' + response.status);
      const markup = await response.text();
      const parsed = new DOMParser().parseFromString(markup, 'text/html');
      const fresh = parsed.getElementById('wall-inner');
      const current = document.getElementById('wall-inner');
      if (fresh && current && fresh.dataset.cut === current.dataset.cut) current.replaceWith(fresh);
    } catch (error) {
      stopped = true;
      const state = document.getElementById('wall-state');
      if (state) state.textContent = 'refresh stopped: ' + error.message;
    }
  }
  setInterval(refresh, every);
})();
"""

RUN_PAGE = """<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8"/>
<meta name="viewport" content="width=device-width, initial-scale=1"/>
<title>sandbox-console · runs</title>
<style>__BASE_CSS__</style>
<style>__RUN_CSS__</style>
</head>
<body>
<header class="app-header">
  <div class="brand"><span class="brand-mark"></span><h1>sandbox-console</h1></div>
  <nav>
    <a href="/">Pool</a>
    <a href="/runs" aria-current="page">Run</a>
  </nav>
  <div class="meta">__META__</div>
  <span class="spacer"></span>
  <a class="btn btn-ghost" href="/healthz">health</a>
</header>
<main>
  __TITLE__
  <div id="wall" data-run="__RUN__" data-live="__LIVE__" data-cut="__CUT__" data-refresh="__REFRESH__">__BODY__</div>
  <p class="hint" id="wall-state">__STATE__</p>
</main>
<script>__JS__</script>
</body>
</html>
"""


def _page(meta: str, title: str, body: str, *, run_id: str = "", live: bool = False, cut: int = 0) -> str:
    state = (
        f"live: re-read every {config.RUN_REFRESH_SECONDS}s from the run database"
        if live
        else "finished run: the wall is drawn from the recorded rows"
    )
    replacements = {
        "__BASE_CSS__": render.BASE_CSS,
        "__RUN_CSS__": RUN_CSS,
        "__META__": meta,
        "__TITLE__": title,
        "__BODY__": body,
        "__RUN__": render.esc(run_id),
        "__LIVE__": "1" if live else "0",
        "__CUT__": str(cut),
        "__REFRESH__": str(config.RUN_REFRESH_SECONDS),
        "__STATE__": render.esc(state),
        "__JS__": RUN_JS,
    }
    markup = RUN_PAGE
    for token, value in replacements.items():
        markup = markup.replace(token, value)
    return markup


def index(refs: list[runs.RunRef]) -> str:
    """The runs index: every database the console can see, newest first."""
    rows = [
        '<div class="run-row head"><span>run</span><span>stored</span><span>instances</span>'
        "<span>outcomes</span><span>ended</span><span>size</span><span>report</span></div>"
    ]
    for ref in refs:
        outcomes = " ".join(f"{name} {count}" for name, count in sorted(ref.outcomes.items()))
        ended = (
            "live"
            if ref.live
            else (_clock(ref.ended_ns) if ref.ended_ns else "no end row")
        )
        rows.append(
            f'<div class="run-row"><a class="mono" href="/runs/{render.esc(ref.run_id)}">'
            f"{render.esc(ref.run_id)}</a>"
            f'<span class="mono">{ref.stored}</span>'
            f'<span class="mono">{ref.instances}</span>'
            f'<span class="muted">{render.esc(outcomes or "—")}</span>'
            f'<span class="muted">{render.esc(ended)}</span>'
            f'<span class="muted mono">{render.fmt_bytes(ref.size)}</span>'
            f'<span class="muted">{"json" if ref.has_report else "db only"}'
            f'{render.esc(" · " + ref.error if ref.error else "")}</span></div>'
        )
    if not refs:
        body = (
            '<div class="empty"><strong>No run database mounted.</strong>'
            f"<span>Nothing was found in <code>{render.esc(str(config.RUNS_DIR))}</code>. "
            "session-lab writes one <code>.db</code> per run; the chart mounts that directory "
            "at <code>/runs</code>.</span></div>"
        )
    else:
        body = "".join(rows)
    title = (
        '<div class="run-title"><h2>Runs</h2>'
        f'<span class="muted">{len(refs)} database(s) in '
        f"<code>{render.esc(str(config.RUNS_DIR))}</code></span></div>"
    )
    return _page(
        '<span class="badge badge-outline">session-lab</span>'
        f'<span class="badge badge-outline">runs {len(refs)}</span>',
        title,
        body,
    )


def run_page(run: runs.Run, cut_ns: int, *, instance: str = "", tiles: dict | None = None) -> str:
    """One run, either the rack or one instance's tracks."""
    body = (
        tracks_page(run, cut_ns, instance)
        if instance
        else wall(run, cut_ns, tiles)
    )
    counts = run.counts(cut_ns)
    state = "live" if run.live else "finished"
    badge = "badge-success" if run.live else "badge-outline"
    title = (
        '<div class="run-title">'
        f'<h2 class="mono">{render.esc(run.run_id)}</h2>'
        f'<span class="badge {badge}">{state}</span>'
        f'<span class="badge badge-outline">{run.planned} planned</span>'
        f'<span class="badge badge-outline">{run.stored} stored</span>'
        f'<span class="badge badge-outline">{run.peak_concurrency} peak in flight</span>'
        f'<span class="badge badge-outline">{run.contexts_per_browser or config.CONTEXTS_PER_BROWSER}'
        " ctx/browser</span>"
        f'<span class="muted">{_stamp(run.started_ns)} → '
        f'{_stamp(run.ended_ns) if run.ended_ns else "running"}</span>'
        "</div>"
    )
    meta = (
        '<span class="badge badge-outline">session-lab</span>'
        f'<span class="badge badge-outline">{render.esc(run.run_id)}</span>'
        f'<span class="badge badge-outline">{counts[runs.HOLDING]} holding</span>'
    )
    return _page(meta, title, body, run_id=run.run_id, live=run.live, cut=cut_ns)
