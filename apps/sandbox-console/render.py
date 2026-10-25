"""Server-rendered HTML: the page, the grid fragment, one card, the dialog.

Design notes, because this file is the whole front end:

* **Tokens, not colours.** The palette is shadcn's dark defaults expressed as HSL
  custom properties (`--background`, `--card`, `--border`, `--primary`,
  `--muted-foreground`, `--destructive`, `--ring`). Re-theming is editing that
  block; nothing else in the file names a colour.
* **No Tailwind, no shadcn package.** Both need a node build step or a CDN, and
  this pane has to render on a LAN with neither. The vocabulary is a small set of
  component classes (`.btn`, `.card`, `.badge`, `.dialog`, `.toast`) built from
  the same tokens, which is what those libraries compile down to anyway.
* **Type and spacing are on a scale.** 12/13/14/16/20 px, 4/8/12/16/24 px, one
  radius, one control height. Numbers are tabular so columns line up.
* **Live data is pushed.** The grid subscribes to `/events` (Server-Sent Events)
  instead of a `setInterval` poll: one connection, updates only when something
  changed, with polling as a fallback. SSE rather than a WebSocket because the
  traffic is one-way and the router cannot carry an upgrade -- the DevTools socket
  lives inside the sandbox for exactly that reason.
* **Every control reports.** A button disables itself while in flight and a toast
  carries the outcome, so "nothing happened" is always a visible error or a
  visible busy state.
"""

from __future__ import annotations

import html
from datetime import datetime, timezone

import config
import inventory


def esc(value) -> str:
    if value is None:
        return ""
    return html.escape(str(value), quote=True)


def fmt_bytes(value: int | None) -> str:
    if not value:
        return "—"
    mib = value / (1024 * 1024)
    if mib >= 1024:
        return f"{mib / 1024:.1f} GiB"
    return f"{mib:.0f} MiB"


def fmt_age(seconds: int | None) -> str:
    if seconds is None:
        return "—"
    seconds = int(seconds)
    if seconds < 60:
        return f"{seconds}s"
    if seconds < 3600:
        return f"{seconds // 60}m"
    hours, remainder = divmod(seconds, 3600)
    return f"{hours}h{remainder // 60:02d}m"


def fmt_cpu(millicores: int | None) -> str:
    if not millicores:
        return "—"
    if millicores >= 1000:
        return f"{millicores / 1000:.1f}"
    return f"{millicores}m"


def _clock(epoch: float) -> str:
    if not epoch:
        return "—"
    return datetime.fromtimestamp(epoch, tz=timezone.utc).astimezone().strftime("%H:%M:%S")


BASE_CSS = """
:root {
  color-scheme: dark;
  --background: 240 10% 3.9%;
  --foreground: 0 0% 98%;
  --card: 240 6% 7%;
  --popover: 240 8% 5%;
  --muted: 240 4% 12%;
  --muted-foreground: 240 5% 65%;
  --border: 240 4% 16%;
  --input: 240 4% 22%;
  --primary: 221 83% 53%;
  --primary-foreground: 0 0% 100%;
  --secondary: 240 4% 14%;
  --destructive: 0 72% 45%;
  --destructive-foreground: 0 0% 100%;
  --success: 142 69% 45%;
  --warning: 38 92% 55%;
  --ring: 221 83% 62%;
  --radius: 8px;
  --control-h: 32px;
  --font-sans: ui-sans-serif, system-ui, -apple-system, "Segoe UI", Roboto, Inter, sans-serif;
  --font-mono: ui-monospace, SFMono-Regular, Menlo, "Cascadia Mono", monospace;
  --text-xs: 12px; --text-sm: 13px; --text-base: 14px; --text-lg: 16px; --text-xl: 20px;
  --space-1: 4px; --space-2: 8px; --space-3: 12px; --space-4: 16px; --space-6: 24px;
}
*, *::before, *::after { box-sizing: border-box; }
html, body { height: 100%; }
body {
  margin: 0; background: hsl(var(--background)); color: hsl(var(--foreground));
  font-family: var(--font-sans); font-size: var(--text-base); line-height: 1.5;
  -webkit-font-smoothing: antialiased;
}
::selection { background: hsl(var(--primary) / .35); }
:focus-visible { outline: 2px solid hsl(var(--ring)); outline-offset: 2px; border-radius: 6px; }
h1, h2, h3 { margin: 0; font-weight: 600; letter-spacing: -.01em; }
a { color: inherit; text-decoration: none; }
.mono { font-family: var(--font-mono); font-variant-numeric: tabular-nums; }
.muted { color: hsl(var(--muted-foreground)); }
.spacer { margin-left: auto; }

.app-header {
  position: sticky; top: 0; z-index: 10;
  display: flex; align-items: center; gap: var(--space-3);
  padding: var(--space-3) var(--space-6);
  background: hsl(var(--background) / .85);
  backdrop-filter: blur(8px);
  border-bottom: 1px solid hsl(var(--border));
}
.brand { display: flex; align-items: center; gap: var(--space-2); }
.brand-mark {
  width: 22px; height: 22px; border-radius: 6px;
  background: linear-gradient(140deg, hsl(var(--primary)), hsl(262 83% 58%));
}
.brand h1 { font-size: var(--text-lg); }
.meta { display: flex; gap: var(--space-2); align-items: center; flex-wrap: wrap; }
main { max-width: 1440px; margin: 0 auto; padding: var(--space-6); }

.btn {
  display: inline-flex; align-items: center; justify-content: center; gap: 6px;
  height: var(--control-h); padding: 0 var(--space-3);
  border-radius: var(--radius); border: 1px solid hsl(var(--input));
  background: hsl(var(--card)); color: hsl(var(--foreground));
  font: 500 var(--text-sm)/1 var(--font-sans); cursor: pointer;
  transition: background .12s ease, border-color .12s ease, opacity .12s ease;
  white-space: nowrap;
}
.btn:hover { background: hsl(var(--secondary)); }
.btn[disabled], .btn[aria-busy="true"] { opacity: .55; cursor: progress; }
.btn-primary { background: hsl(var(--primary)); border-color: hsl(var(--primary)); color: hsl(var(--primary-foreground)); }
.btn-primary:hover { background: hsl(var(--primary) / .9); }
.btn-outline { background: transparent; }
.btn-ghost { background: transparent; border-color: transparent; color: hsl(var(--muted-foreground)); }
.btn-ghost:hover { background: hsl(var(--secondary)); color: hsl(var(--foreground)); }
.btn-sm { height: 26px; padding: 0 var(--space-2); font-size: var(--text-xs); }

.toolbar { display: flex; align-items: center; gap: var(--space-2); flex-wrap: wrap; padding-bottom: var(--space-4); }
.separator { width: 1px; height: 20px; background: hsl(var(--border)); margin: 0 var(--space-1); }

.badge {
  display: inline-flex; align-items: center; gap: 6px;
  padding: 2px 8px; border-radius: 999px;
  border: 1px solid hsl(var(--border)); background: hsl(var(--muted));
  font-size: var(--text-xs); font-weight: 500; color: hsl(var(--muted-foreground)); white-space: nowrap;
}
.badge .dot { width: 6px; height: 6px; border-radius: 999px; background: currentColor; }
.badge-success { color: hsl(var(--success)); border-color: hsl(var(--success) / .35); background: hsl(var(--success) / .12); }
.badge-warning { color: hsl(var(--warning)); border-color: hsl(var(--warning) / .35); background: hsl(var(--warning) / .12); }
.badge-outline { background: transparent; }

.stats { display: flex; gap: var(--space-4); flex-wrap: wrap; margin-bottom: var(--space-4); }
.stat { display: flex; flex-direction: column; gap: 2px; min-width: 78px; }
.stat-label { font-size: var(--text-xs); color: hsl(var(--muted-foreground)); text-transform: uppercase; letter-spacing: .04em; }
.stat-value { font-size: var(--text-xl); font-weight: 600; font-variant-numeric: tabular-nums; }
#stream-state { color: hsl(var(--success)); }

.grid { display: grid; gap: var(--space-4); grid-template-columns: repeat(auto-fill, minmax(288px, 1fr)); }
.card {
  display: flex; flex-direction: column; background: hsl(var(--card));
  border: 1px solid hsl(var(--border)); border-radius: calc(var(--radius) * 1.5);
  overflow: hidden; transition: border-color .12s ease, box-shadow .12s ease, transform .12s ease;
}
.card[role="button"] { cursor: pointer; }
.card[role="button"]:hover { border-color: hsl(var(--input)); box-shadow: 0 8px 24px hsl(0 0% 0% / .35); transform: translateY(-1px); }
.shot { position: relative; aspect-ratio: 16 / 9; background: hsl(240 10% 2%); }
.shot img { width: 100%; height: 100%; object-fit: cover; display: block; }
.shot .placeholder {
  position: absolute; inset: 0; display: none; align-items: center; justify-content: center;
  font-size: var(--text-xs); color: hsl(var(--muted-foreground));
}
.shot.is-broken img { display: none; }
.shot.is-broken .placeholder { display: flex; }
.shot .rtt {
  position: absolute; right: var(--space-2); bottom: var(--space-2); padding: 1px 6px;
  border-radius: 999px; background: hsl(var(--popover) / .85); border: 1px solid hsl(var(--border));
  font-size: 11px; color: hsl(var(--muted-foreground)); font-variant-numeric: tabular-nums;
}
.card-head { display: flex; align-items: center; gap: var(--space-2); padding: var(--space-3) var(--space-3) 0; }
.card-title { font-size: var(--text-sm); font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.card-head .badge { margin-left: auto; }
.card-body { padding: var(--space-2) var(--space-3) var(--space-3); display: grid; gap: var(--space-2); }
.facts { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: var(--space-1) var(--space-3); margin: 0; }
.fact { display: flex; justify-content: space-between; gap: var(--space-2); font-size: var(--text-xs); }
.fact dt { color: hsl(var(--muted-foreground)); }
.fact dd { margin: 0; font-family: var(--font-mono); font-variant-numeric: tabular-nums; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.chips { display: flex; flex-wrap: wrap; gap: 6px; align-items: center; }
.chip {
  display: inline-flex; align-items: baseline; gap: 5px; padding: 1px 7px; border-radius: 6px;
  border: 1px solid hsl(var(--border)); font-size: 11px; color: hsl(var(--muted-foreground));
}
.chip b { font-weight: 600; color: inherit; }
.chip i { font-style: normal; opacity: .75; }
.chip.good { color: hsl(var(--success)); border-color: hsl(var(--success) / .35); background: hsl(var(--success) / .1); }
.chip.bad { color: hsl(0 84% 68%); border-color: hsl(0 84% 60% / .35); background: hsl(0 84% 60% / .1); }
.chip.muted { color: hsl(var(--muted-foreground)); }
.hint, .note { margin: 0; font-size: var(--text-xs); color: hsl(var(--muted-foreground)); }
.note { color: hsl(var(--warning)); }
.card-foot {
  display: flex; align-items: center; gap: var(--space-2); padding: var(--space-2) var(--space-3);
  margin-top: auto; border-top: 1px solid hsl(var(--border)); font-size: var(--text-xs);
}
.empty {
  display: flex; flex-direction: column; gap: var(--space-2); align-items: flex-start;
  padding: var(--space-6); border: 1px dashed hsl(var(--border));
  border-radius: calc(var(--radius) * 1.5); color: hsl(var(--muted-foreground));
  font-size: var(--text-sm); max-width: 80ch;
}
.errors {
  margin: 0 0 var(--space-4); padding: var(--space-2) var(--space-3);
  border: 1px solid hsl(var(--destructive) / .5); border-radius: var(--radius);
  background: hsl(var(--destructive) / .1); color: hsl(0 84% 72%);
  font-size: var(--text-xs); list-style: none;
}

.dialog-backdrop {
  position: fixed; inset: 0; z-index: 50; background: hsl(240 10% 2% / .8);
  backdrop-filter: blur(4px); display: flex; align-items: center; justify-content: center;
  padding: var(--space-6);
}
.dialog {
  width: min(1200px, 96vw); max-height: 92vh; overflow: auto; background: hsl(var(--popover));
  border: 1px solid hsl(var(--border)); border-radius: calc(var(--radius) * 1.5);
  box-shadow: 0 24px 64px hsl(0 0% 0% / .5); display: flex; flex-direction: column;
}
.dialog-head {
  display: flex; align-items: center; gap: var(--space-3); padding: var(--space-3) var(--space-4);
  border-bottom: 1px solid hsl(var(--border)); position: sticky; top: 0;
  background: hsl(var(--popover)); z-index: 1;
}
.dialog-head h2 { font-size: var(--text-base); }
.dialog-body { padding: var(--space-4); display: grid; gap: var(--space-3); }
.dialog img.big {
  width: 100%; height: auto; border-radius: var(--radius);
  background: hsl(240 10% 2%); cursor: crosshair; display: block;
}
.big-placeholder {
  display: none; align-items: center; justify-content: center; height: 320px;
  border: 1px dashed hsl(var(--border)); border-radius: var(--radius);
  color: hsl(var(--muted-foreground)); font-size: var(--text-sm);
}
.dialog.is-broken img.big { display: none; }
.dialog.is-broken .big-placeholder { display: flex; }

#toasts {
  position: fixed; right: var(--space-4); bottom: var(--space-4); z-index: 60;
  display: flex; flex-direction: column; gap: var(--space-2); max-width: 46ch;
}
.toast {
  padding: var(--space-2) var(--space-3); border-radius: var(--radius);
  border: 1px solid hsl(var(--border)); border-left: 3px solid hsl(var(--primary));
  background: hsl(var(--popover)); font-size: var(--text-sm); animation: toast-in .16s ease-out;
}
.toast.is-error { border-left-color: hsl(0 84% 60%); }
@keyframes toast-in { from { opacity: 0; transform: translateY(6px); } }

.stream {
  margin-top: var(--space-4); border: 1px solid hsl(var(--border));
  border-radius: calc(var(--radius) * 1.5); background: hsl(var(--card)); display: none; overflow: hidden;
}
.stream.is-open { display: block; }
.stream header {
  display: flex; align-items: center; gap: var(--space-2); padding: var(--space-2) var(--space-3);
  border-bottom: 1px solid hsl(var(--border)); font-size: var(--text-sm); font-weight: 600;
}
.stream pre {
  margin: 0; padding: var(--space-3); max-height: 240px; overflow: auto;
  font-family: var(--font-mono); font-size: var(--text-xs); line-height: 1.6;
  color: hsl(var(--muted-foreground)); white-space: pre-wrap;
}
.stream-live { margin-left: auto; font-size: var(--text-xs); font-weight: 400; color: hsl(var(--muted-foreground)); }

@media (max-width: 640px) {
  main { padding: var(--space-4); }
  .app-header { padding: var(--space-2) var(--space-4); }
}
@media (prefers-reduced-motion: reduce) { * { animation: none !important; transition: none !important; } }
"""


def isolation_badges(tile: inventory.Tile) -> str:
    """Two live claims and two spec claims, each carrying its evidence."""
    reach = tile.reachability
    if not reach:
        return (
            '<div class="chips"><span class="chip muted">isolation not probed</span>'
            f'<button class="btn btn-ghost btn-sm" data-act="probe" data-id="{esc(tile.id)}">probe</button>'
            "</div>"
        )
    evidence = reach.get("evidence") or {}
    public = "allowed" if reach.get("egress_public_ok") else "blocked"
    chips = [
        _chip("cluster", str(reach.get("cluster", "unknown")), _class_for(str(reach.get("cluster")), "blocked"), _detail(evidence.get("cluster"))),
        _chip("metadata", str(reach.get("metadata", "unknown")), _class_for(str(reach.get("metadata")), "blocked"), _detail(evidence.get("metadata"))),
        _chip("egress", public, _class_for(public, "allowed"), _detail(evidence.get("public"))),
        _chip("sa token", str(reach.get("sa_token", "unknown")), _class_for(str(reach.get("sa_token")), "absent"), "spec", title="read from the pod spec"),
        _chip("dns", str(reach.get("cluster_dns", "unknown")), _class_for(str(reach.get("cluster_dns")), "none"), "spec", title="read from the pod spec"),
    ]
    return (
        f'<div class="chips">{"".join(chips)}'
        f'<button class="btn btn-ghost btn-sm" data-act="probe" data-id="{esc(tile.id)}">re-probe</button></div>'
    )


def _class_for(value: str, good: str) -> str:
    """`blocked` is the good outcome for isolation; `reachable` is not."""
    if value in {"unknown", ""}:
        return "muted"
    if value.startswith(good) or value == "absent" or value.startswith("none"):
        return "good"
    return "bad"


def _chip(label: str, value: str, css: str, detail: str, title: str = "") -> str:
    title_attr = f' title="{esc(title)}"' if title else ""
    detail_markup = f"<i>{esc(detail)}</i>" if detail else ""
    return f'<span class="chip {css}"{title_attr}><b>{esc(label)}</b> {esc(value)}{detail_markup}</span>'


def _detail(evidence) -> str:
    if not isinstance(evidence, dict):
        return ""
    parts = []
    if evidence.get("ms") is not None:
        parts.append(f"{evidence['ms']} ms")
    if evidence.get("outcome"):
        parts.append(str(evidence["outcome"]))
    return " · ".join(parts)


def phase_badge(tile: inventory.Tile) -> str:
    css = "badge-success" if tile.ready else "badge-warning"
    return f'<span class="badge {css}"><span class="dot"></span>{esc(tile.phase)}</span>'


def tile(tile: inventory.Tile) -> str:
    """One card.

    Clicking anywhere on the card opens the dialog (RFC §6.3); the click that
    drives the page is dispatched from inside the dialog, where the image is large
    enough to aim at and the display scale is known.
    """
    shot_url = f"/sandboxes/{esc(tile.id)}/shot?v={tile.screenshot_ts}"
    facts = [
        ("claim", tile.claim),
        ("template", tile.template),
        ("ip", tile.pod_ip),
        ("mem", fmt_bytes(tile.mem_working_set_bytes)),
        ("cpu", fmt_cpu(tile.cpu_millicores)),
        ("age", fmt_age(tile.age_s)),
    ]
    fact_rows = "".join(
        f'<div class="fact"><dt>{esc(label)}</dt>'
        f'<dd title="{esc(value if value is not None else "—")}">{esc(value if value is not None else "—")}</dd></div>'
        for label, value in facts
    )
    note = f'<p class="note">{esc(tile.note)}</p>' if tile.note else ""
    hint = (
        ""
        if tile.ready
        else '<p class="hint">not Ready — a frame appears once the readiness probe on :9222 answers</p>'
    )
    return f"""<article class="card" id="tile-{esc(tile.id)}" data-act="expand" data-id="{esc(tile.id)}"
         role="button" tabindex="0" aria-label="open {esc(tile.id)}">
  <div class="shot">
    <img src="{shot_url}" alt="live screenshot of {esc(tile.id)}" loading="lazy"/>
    <div class="placeholder">no frame yet</div>
    <span class="rtt" data-rtt="{esc(tile.id)}"></span>
  </div>
  <div class="card-head">
    <span class="card-title mono" title="{esc(tile.id)}">{esc(tile.id)}</span>
    {phase_badge(tile)}
  </div>
  <div class="card-body">
    <dl class="facts">{fact_rows}</dl>
    {hint}{note}
    {isolation_badges(tile)}
  </div>
  <div class="card-foot">
    <span class="muted">{esc(tile.launch_type or tile.created_by or "")}</span>
    <span class="spacer"></span>
    <span class="muted">click to expand</span>
    <button class="btn btn-ghost btn-sm" data-act="delete" data-id="{esc(tile.id)}"
            title="delete the claim that owns this sandbox">delete</button>
  </div>
</article>"""


def action_bar(write_enabled: bool, allow_raw: bool) -> str:
    """The controls that mutate. Every one reports success or failure."""
    if not write_enabled:
        return '<div class="toolbar"><span class="badge badge-outline">read-only · WRITE_ENABLED is off</span></div>'
    raw = (
        '<button class="btn btn-outline" data-act="create" data-kind="sandbox">+ bare sandbox</button>'
        if allow_raw
        else ""
    )
    return (
        '<div class="toolbar" role="toolbar" aria-label="actions">'
        '<button class="btn btn-primary" data-act="create" data-kind="claim">+ claim</button>'
        f"{raw}"
        '<span class="separator" role="separator"></span>'
        '<button class="btn btn-outline" data-act="demo-fill">fill demo grid</button>'
        '<button class="btn btn-outline" data-act="demo-teardown">teardown</button>'
        '<a class="btn btn-outline" href="/compare">compare</a>'
        '<span class="separator" role="separator"></span>'
        '<button class="btn btn-ghost" data-act="recipe" data-recipe="claim-burst">claim-burst</button>'
        '<button class="btn btn-ghost" data-act="recipe" data-recipe="capacity-check">capacity</button>'
        '<span class="spacer"></span>'
        f'<span class="badge badge-outline">ceiling {config.MAX_CONCURRENT_SANDBOXES}</span>'
        f'<span class="badge badge-outline">demo {config.DEMO_TILES}</span>'
        "</div>"
    )


def stats(report: inventory.Report) -> str:
    items = [
        ("running", str(report.running)),
        ("pending", str(report.pending)),
        ("pool", f"{report.pool_ready}/{report.pool_idle}"),
        ("resident", fmt_bytes(report.mem_working_set_bytes)),
        ("cpu", fmt_cpu(report.cpu_millicores)),
        ("as of", _clock(report.collected_at)),
    ]
    cells = "".join(
        f'<div class="stat"><span class="stat-label">{esc(label)}</span>'
        f'<span class="stat-value">{esc(value)}</span></div>'
        for label, value in items
    )
    return (
        f'<div class="stats">{cells}'
        '<div class="stat"><span class="stat-label">stream</span>'
        '<span class="stat-value" id="stream-state" style="font-size:var(--text-base)">connecting</span></div></div>'
    )


def grid(report: inventory.Report) -> str:
    """The fragment pushed over /events: toolbar, stats, errors, cards."""
    toolbar = action_bar(config.WRITE_ENABLED, config.ALLOW_RAW_SANDBOX)
    errors = ""
    if report.errors:
        errors = '<ul class="errors">' + "".join(f"<li>{esc(item)}</li>" for item in report.errors) + "</ul>"
    if report.tiles:
        cards = "".join(tile(item) for item in report.tiles)
    else:
        cards = (
            '<div class="empty"><strong>No claimed sandbox yet.</strong>'
            "<span>Press <em>claim</em> to adopt one from the warm pool. Idle pool members cannot be "
            "routed by name, so they are counted in the stats above and never drawn as cards.</span></div>"
        )
    truncated = (
        f'<p class="hint" style="margin-top:var(--space-3)">{report.truncated} more not shown '
        f"(MAX_TILES={config.MAX_TILES})</p>"
        if report.truncated
        else ""
    )
    return (
        f'<div id="grid-inner" data-signature="{esc(signature(report))}">'
        f"{toolbar}{stats(report)}{errors}<div class=\"grid\">{cards}</div>{truncated}</div>"
    )


def signature(report: inventory.Report) -> str:
    """Cheap fingerprint of the grid, so /events pushes only real changes."""
    parts = [str(report.running), str(report.pending), str(report.pool_ready), str(report.pool_idle)]
    for item in report.tiles:
        parts.append(
            "|".join([
                item.id,
                item.phase,
                str(item.age_s),
                str(item.screenshot_ts),
                str(item.mem_working_set_bytes),
                str(bool(item.reachability)),
                str(item.note or ""),
            ])
        )
    return ";".join(parts)


def expanded(tile_obj: inventory.Tile) -> str:
    """The dialog: the frame scaled up, and the only place a tap is dispatched."""
    reason = ""
    if not tile_obj.ready:
        reason = (
            f'<p class="note">sandbox is <b>{esc(tile_obj.phase)}</b>, not Ready — '
            "there is no frame to expand yet</p>"
        )
    extra = f'<p class="note">{esc(tile_obj.note)}</p>' if tile_obj.note else ""
    return f"""<div class="dialog-backdrop" id="dialog">
  <div class="dialog" role="dialog" aria-modal="true" aria-labelledby="dialog-title"
       data-id="{esc(tile_obj.id)}" data-width="{config.VIEWPORT_WIDTH}">
    <div class="dialog-head">
      <h2 id="dialog-title" class="mono">{esc(tile_obj.id)}</h2>
      <span class="badge badge-outline">{esc(tile_obj.phase)}</span>
      <span class="muted mono">{esc(tile_obj.pod_ip or "")} · {esc(fmt_age(tile_obj.age_s))}</span>
      <span class="spacer"></span>
      <button class="btn btn-ghost btn-sm" data-act="close">close</button>
    </div>
    <div class="dialog-body">
      <img class="big" src="/sandboxes/{esc(tile_obj.id)}/shot?v={tile_obj.screenshot_ts}"
           alt="expanded live screenshot of {esc(tile_obj.id)}"/>
      <div class="big-placeholder">no frame yet</div>
      {reason}{extra}
      {isolation_badges(tile_obj)}
      <p class="hint">A click on the frame is dispatched as a real mouse press/release, divided by the display scale. Viewport stays {config.VIEWPORT_WIDTH}×{config.VIEWPORT_HEIGHT}.</p>
    </div>
  </div>
</div>"""


def _bar(label: str, value: int | None, scale: int, median: int | None) -> str:
    value = value or 0
    width = 0 if not scale else max(2, int(round(100 * value / scale)))
    hint = f"p50 of last three: {median} ms" if median else ""
    return (
        f'<div class="fact" title="{esc(hint)}" style="display:grid;'
        'grid-template-columns:88px 1fr 64px;gap:var(--space-2);align-items:center">'
        f"<dt>{esc(label)}</dt>"
        '<span class="bar" style="height:10px;border-radius:999px;background:hsl(var(--muted));overflow:hidden;display:block">'
        f'<i style="display:block;height:100%;width:{width}%;background:hsl(var(--primary))"></i></span>'
        f'<dd style="text-align:right">{value / 1000:.2f} s</dd></div>'
    )


def compare_page(latest, medians: dict, allow_raw: bool, write_enabled: bool) -> str:
    """The split view: two columns on one shared scale, reachability underneath."""
    if latest is None:
        body = (
            '<div class="empty"><strong>No comparison has run yet.</strong>'
            "<span>One press fires a warm Claim and a bare Sandbox in the same second and times "
            "both: scheduled, ready, CDP up, first paint.</span></div>"
        )
    else:
        scale = max([arm.total_ms() for arm in latest.arms] or [0])
        columns = []
        for arm in latest.arms:
            arm_medians = medians.get(arm.label, {})
            timers = "".join(
                _bar(label, arm.timers.get(key), scale, arm_medians.get(key))
                for label, key in (
                    ("scheduled", "t_sched_ms"),
                    ("ready", "t_ready_ms"),
                    ("cdp up", "t_cdp_ms"),
                    ("first paint", "t_paint_ms"),
                )
            )
            reach = ""
            if isinstance(arm.reachability, dict):
                reach = '<div class="chips">' + "".join(
                    _chip(key, str(value), _class_for(str(value), "blocked"), "")
                    for key, value in arm.reachability.items()
                ) + "</div>"
            error = f'<p class="note">{esc(arm.error)}</p>' if arm.error else ""
            columns.append(
                f'<section class="card col" style="cursor:default">'
                f'<div class="card-head"><span class="card-title mono">{esc(arm.sandbox)}</span>'
                f'<span class="badge badge-outline">{esc(arm.label)}</span></div>'
                f'<div class="card-body">{timers}{reach}{error}</div>'
                '<div class="card-foot"><span class="muted">p50 of last three in each tooltip</span>'
                '<span class="spacer"></span>'
                f'<button class="btn btn-ghost btn-sm" data-act="delete" data-id="{esc(arm.sandbox)}">delete</button>'
                "</div></section>"
            )
        body = '<div class="grid" style="grid-template-columns:1fr 1fr">' + "".join(columns) + "</div>"
        if latest.note:
            body += f'<p class="note" style="margin-top:var(--space-4)">{esc(latest.note)}</p>'

    note = (
        ""
        if allow_raw
        else '<p class="hint" style="margin-top:var(--space-4)">allowRawSandbox is off: the bare '
        "column needs it, because an unhardened Sandbox is the thing being compared.</p>"
    )
    button = (
        '<button class="btn btn-primary" data-act="compare-run">run compare</button>'
        if write_enabled
        else '<span class="badge badge-outline">read-only · WRITE_ENABLED is off</span>'
    )
    return f"""<!doctype html>
<html lang="en"><head>
<meta charset="utf-8"/><meta name="viewport" content="width=device-width, initial-scale=1"/>
<title>compare · sandbox-console</title>
<style>{BASE_CSS}</style></head>
<body>
<header class="app-header">
  <div class="brand"><span class="brand-mark"></span><h1>compare</h1></div>
  <div class="meta"><span class="badge badge-outline">same image · same URL · fired together</span></div>
  <span class="spacer"></span>
  <div class="meta">{button}<a class="btn btn-ghost" href="/">grid</a></div>
</header>
<main>{body}{note}<p class="hint" id="compare-status" role="status"></p></main>
<div id="toasts" aria-live="polite"></div>
<script>{COMPARE_JS}</script>
</body></html>"""


COMPARE_JS = """
"use strict";
(function () {
  var toasts = document.getElementById("toasts");
  var status = document.getElementById("compare-status");

  function toast(message, isError) {
    var node = document.createElement("div");
    node.className = "toast" + (isError ? " is-error" : "");
    node.textContent = message;
    toasts.appendChild(node);
    setTimeout(function () { node.remove(); }, 6000);
  }

  document.addEventListener("click", async function (event) {
    var node = event.target.closest("[data-act]");
    if (!node) return;
    var act = node.dataset.act;
    if (act === "delete") {
      var id = node.dataset.id;
      if (!window.confirm("Delete " + id + "?")) return;
      node.setAttribute("aria-busy", "true");
      try {
        var response = await fetch("/sandboxes/" + id, { method: "DELETE" });
        var body = await response.text();
        if (!response.ok) throw new Error(body || "HTTP " + response.status);
        toast("deleted " + id);
        status.textContent = "deleted " + id;
      } catch (error) {
        toast("delete failed: " + error.message, true);
        status.textContent = "delete failed: " + error.message;
      } finally {
        node.removeAttribute("aria-busy");
      }
      return;
    }
    if (act === "compare-run") {
      node.setAttribute("aria-busy", "true");
      node.disabled = true;
      status.textContent = "firing one claim and one bare sandbox - up to ~90 s";
      try {
        var run = await fetch("/compare", { method: "POST" });
        var payload = await run.text();
        if (!run.ok) throw new Error(payload || "HTTP " + run.status);
        window.location.reload();
      } catch (error) {
        status.textContent = "compare failed: " + error.message;
        toast("compare failed: " + error.message, true);
        node.removeAttribute("aria-busy");
        node.disabled = false;
      }
    }
  });
})();
"""


PAGE_JS = """
"use strict";
(function () {
  var VIEWPORT = {w: 1280, h: 600};
  var grid = document.getElementById("grid");
  var toasts = document.getElementById("toasts");
  var source = null;
  var fallbackTimer = null;
  var inFlight = 0;

  function toast(message, isError) {
    var node = document.createElement("div");
    node.className = "toast" + (isError ? " is-error" : "");
    node.textContent = message;
    toasts.appendChild(node);
    setTimeout(function () { node.remove(); }, 6000);
  }

  function noteStream(state) {
    var badge = document.getElementById("stream-state");
    if (badge) badge.textContent = state;
  }

  function connect() {
    if (typeof EventSource === "undefined") { startFallback(); return; }
    source = new EventSource("/events");
    source.addEventListener("grid", function (event) {
      swapGrid(event.data);
      noteStream("live");
    });
    source.addEventListener("heartbeat", function () { noteStream("live"); });
    source.onerror = function () {
      noteStream("reconnecting");
      setTimeout(function () {
        if (source && source.readyState === EventSource.CLOSED) startFallback();
      }, 5000);
    };
  }

  function startFallback() {
    if (fallbackTimer) return;
    noteStream("polling");
    var poll = async function () {
      try {
        var response = await fetch("/sandboxes", {cache: "no-store"});
        if (!response.ok) throw new Error("HTTP " + response.status);
        swapGrid(await response.text());
        noteStream("polling");
      } catch (error) {
        noteStream("offline");
      }
    };
    poll();
    fallbackTimer = setInterval(poll, 3000);
  }

  function swapGrid(markup) {
    // Never yank the DOM while a dialog is open or an action is in flight.
    if (!markup || inFlight > 0 || document.getElementById("dialog")) return;
    var holder = document.createElement("div");
    holder.innerHTML = markup;
    var next = holder.firstElementChild;
    if (next && next.id === "grid-inner") grid.replaceChildren(next);
  }

  async function refreshNow() {
    try {
      var response = await fetch("/sandboxes", {cache: "no-store"});
      if (response.ok) swapGrid(await response.text());
    } catch (error) { /* the stream will catch up */ }
  }

  async function mutate(path, options, label) {
    inFlight += 1;
    try {
      var response = await fetch(path, options);
      var body = await response.text();
      if (!response.ok) {
        var detail = body;
        try { detail = JSON.parse(body).detail || body; } catch (error) { /* plain text */ }
        throw new Error(detail || "HTTP " + response.status);
      }
      toast(label);
      return body;
    } catch (error) {
      toast(error.message, true);
      return null;
    } finally {
      inFlight -= 1;
    }
  }

  async function openDialog(id) {
    try {
      var response = await fetch("/sandboxes/" + id + "/expanded", {cache: "no-store"});
      if (!response.ok) throw new Error("HTTP " + response.status);
      var holder = document.createElement("div");
      holder.innerHTML = await response.text();
      var backdrop = holder.firstElementChild;
      document.body.appendChild(backdrop);
      var closeButton = backdrop.querySelector("[data-act='close']");
      if (closeButton) closeButton.focus();
    } catch (error) {
      toast("could not open " + id + ": " + error.message, true);
    }
  }

  function closeDialog() {
    var dialog = document.getElementById("dialog");
    if (dialog) dialog.remove();
  }

  async function probe(id, node) {
    if (node) node.setAttribute("aria-busy", "true");
    var body = await mutate("/sandboxes/" + id + "/reachability", {method: "POST"}, "probed " + id);
    if (body) { closeDialog(); await refreshNow(); }
    if (node) node.removeAttribute("aria-busy");
  }

  async function tap(id, base, rect, clientX, clientY) {
    var scale = rect.width / VIEWPORT.w;
    var body = await mutate("/sandboxes/" + id + "/click", {
      method: "POST",
      headers: {"Content-Type": "application/json"},
      body: JSON.stringify({x: clientX - rect.left, y: clientY - rect.top, scale: scale})
    }, "clicked " + id);
    if (body) {
      var image = document.querySelector(".dialog img.big");
      if (image) image.src = base + "?v=" + Date.now();
      await refreshNow();
    }
  }

  async function runRecipe(name, trigger) {
    var panel = document.getElementById("stream");
    var out = document.getElementById("stream-out");
    panel.classList.add("is-open");
    out.textContent = "running " + name + "...\\n";
    if (trigger) trigger.setAttribute("aria-busy", "true");
    try {
      var response = await fetch("/recipes/" + name, {method: "POST"});
      if (!response.ok) throw new Error(await response.text());
      var reader = response.body.getReader();
      var decoder = new TextDecoder();
      var buffer = "";
      for (;;) {
        var chunk = await reader.read();
        if (chunk.done) break;
        buffer += decoder.decode(chunk.value, {stream: true});
        var lines = buffer.split("\\n");
        buffer = lines.pop();
        for (var index = 0; index < lines.length; index += 1) {
          var line = lines[index].trim();
          if (!line) continue;
          var item = {};
          try { item = JSON.parse(line); } catch (error) { continue; }
          if (item.line) out.textContent += item.line + "\\n";
          out.scrollTop = out.scrollHeight;
        }
      }
    } catch (error) {
      out.textContent += "failed: " + error.message + "\\n";
      toast("recipe failed: " + error.message, true);
    } finally {
      if (trigger) trigger.removeAttribute("aria-busy");
      refreshNow();
    }
  }

  document.addEventListener("click", function (event) {
    var action = event.target.closest("[data-act]");
    if (action) {
      var act = action.dataset.act;
      var id = action.dataset.id;
      if (act === "delete") {
        event.stopPropagation();
        if (!window.confirm("Delete " + id + "? This removes the claim that owns it.")) return;
        mutate("/sandboxes/" + id, {method: "DELETE"}, "deleted " + id).then(refreshNow);
        return;
      }
      if (act === "create") {
        var kind = action.dataset.kind || "claim";
        action.setAttribute("aria-busy", "true");
        mutate("/sandboxes", {
          method: "POST",
          headers: {"Content-Type": "application/json"},
          body: JSON.stringify({kind: kind})
        }, "created " + kind).then(function () {
          action.removeAttribute("aria-busy");
          refreshNow();
        });
        return;
      }
      if (act === "demo-fill") {
        action.setAttribute("aria-busy", "true");
        mutate("/demo", {method: "POST"}, "filling the grid").then(function () {
          action.removeAttribute("aria-busy");
          refreshNow();
        });
        return;
      }
      if (act === "demo-teardown") {
        if (!window.confirm("Delete every claim and bare sandbox this pane created?")) return;
        action.setAttribute("aria-busy", "true");
        mutate("/demo/teardown", {method: "POST"}, "torn down").then(function () {
          action.removeAttribute("aria-busy");
          refreshNow();
        });
        return;
      }
      if (act === "recipe") { runRecipe(action.dataset.recipe, action); return; }
      if (act === "probe") { probe(id, action); return; }
      if (act === "expand") { openDialog(id); return; }
      if (act === "close") { closeDialog(); return; }
    }

    if (event.target.classList && event.target.classList.contains("dialog-backdrop")) {
      closeDialog();
      return;
    }

    var big = event.target.closest ? event.target.closest("img.big") : null;
    if (big) {
      var dialog = big.closest(".dialog");
      var rect = big.getBoundingClientRect();
      if (dialog && rect.width) {
        tap(dialog.dataset.id, big.src.split("?")[0], rect, event.clientX, event.clientY);
      }
    }
  });

  document.addEventListener("keydown", function (event) {
    if (event.key === "Escape") closeDialog();
    if ((event.key === "Enter" || event.key === " ") &&
        event.target.matches("[data-act='expand']")) {
      event.preventDefault();
      openDialog(event.target.dataset.id);
    }
  });

  document.addEventListener("error", function (event) {
    var image = event.target;
    if (!image || image.tagName !== "IMG") return;
    var shot = image.closest(".shot");
    if (shot) shot.classList.add("is-broken");
    var dialog = image.closest(".dialog");
    if (dialog) dialog.classList.add("is-broken");
  }, true);

  connect();
  window.addEventListener("beforeunload", function () {
    if (source) source.close();
    if (fallbackTimer) clearInterval(fallbackTimer);
  });
})();
"""


PAGE = """<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8"/>
<meta name="viewport" content="width=device-width, initial-scale=1"/>
<title>sandbox-console</title>
<style>__BASE_CSS__</style>
</head>
<body>
<header class="app-header">
  <div class="brand"><span class="brand-mark"></span><h1>sandbox-console</h1></div>
  <div class="meta">
    <span class="badge badge-outline">agent-sandbox</span>
    <span class="badge badge-outline">__NAMESPACE__</span>
    <span class="badge badge-outline" title="__ROUTER__">router</span>
  </div>
  <span class="spacer"></span>
  <div class="meta"><a class="btn btn-ghost" href="/healthz">health</a></div>
</header>
<main>
  <div id="grid">__GRID__</div>
  <section class="stream" id="stream" aria-live="polite">
    <header>recipe output <span class="stream-live">streaming</span></header>
    <pre id="stream-out"></pre>
  </section>
</main>
<div id="toasts" aria-live="polite"></div>
<script>__PAGE_JS__</script>
</body>
</html>
"""


def page(report: inventory.Report) -> str:
    """Fill the shell by token replacement so CSS and JS braces stay literal."""
    replacements = {
        "__BASE_CSS__": BASE_CSS,
        "__NAMESPACE__": esc(config.SANDBOX_NAMESPACE),
        "__ROUTER__": esc(config.ROUTER_URL),
        "__GRID__": grid(report),
        "__PAGE_JS__": PAGE_JS,
    }
    html_text = PAGE
    for token, value in replacements.items():
        html_text = html_text.replace(token, value)
    return html_text
