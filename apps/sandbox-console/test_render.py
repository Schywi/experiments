"""Render tests: what the tile and the grid actually put on the page."""

import re
import shutil
import subprocess
from datetime import datetime, timezone

import pytest

import fixtures
import inventory
import render

NOW = datetime(2026, 10, 24, 17, 14, 23, tzinfo=timezone.utc)


def report_with(**overrides):
    report = inventory.build_report(
        fixtures.copy_of(fixtures.READY_SANDBOX, fixtures.PENDING_SANDBOX),
        claims=fixtures.copy_of(fixtures.CLAIM),
        pods=fixtures.copy_of(*fixtures.PODS),
        usage=dict(fixtures.USAGE),
        now=NOW,
    )
    for key, value in overrides.items():
        setattr(report, key, value)
    return report


def tile_with(**changes):
    tile = report_with().tiles[0]
    return tile.replace(**changes) if changes else tile


def test_tile_carries_the_shot_url_with_its_timestamp():
    markup = render.tile(tile_with(pod_ip="10.42.0.36"))
    assert 'src="/sandboxes/sb-4f2a/shot?v=0"' in markup
    assert 'id="tile-sb-4f2a"' in markup
    assert "10.42.0.36" in markup
    assert "demo-3" in markup
    assert "browser-tpl" in markup
    assert "250 MiB" in markup
    assert "42m" in markup


def test_shot_url_changes_only_when_a_new_frame_is_stored():
    stale = render.tile(tile_with(screenshot_ts=0))
    fresh = render.tile(tile_with(screenshot_ts=1761316800))
    assert "shot?v=0" in stale
    assert "shot?v=1761316800" in fresh


def test_unprobed_tile_offers_a_probe_and_claims_nothing():
    markup = render.tile(tile_with(reachability=None))
    assert "isolation not probed" in markup
    assert 'data-act="probe"' in markup
    assert "blocked" not in markup


def test_blocked_cluster_badge_is_marked_good_and_shows_its_evidence():
    reachability = {
        "cluster": "blocked",
        "metadata": "blocked",
        "sa_token": "absent",
        "cluster_dns": "none:8.8.8.8,1.1.1.1",
        "egress_public_ok": True,
        "evidence": {
            "cluster": {"ms": 4002, "outcome": "timeout", "error": "AbortError", "source": "live"},
            "metadata": {"ms": 4001, "outcome": "timeout", "error": "AbortError", "source": "live"},
            "public": {"ms": 38, "outcome": "ok", "error": None, "source": "live"},
            "sa_token": "spec",
            "cluster_dns": "spec",
        },
    }
    markup = render.tile(tile_with(reachability=reachability))
    assert 'class="chip good"' in markup
    assert "<b>cluster</b> blocked" in markup
    assert "4002 ms · timeout" in markup
    # The spec-derived claims say where they came from.
    assert 'title="read from the pod spec"' in markup
    assert "<b>sa token</b> absent" in markup


def test_a_reachable_cluster_badge_is_marked_bad():
    reachability = {
        "cluster": "reachable",
        "metadata": "reachable",
        "sa_token": "mounted",
        "cluster_dns": "clusterfirst",
        "egress_public_ok": True,
        "evidence": {"cluster": {"ms": 12, "outcome": "refused", "error": "TypeError", "source": "live"}},
    }
    markup = render.tile(tile_with(reachability=reachability))
    assert 'class="chip bad"' in markup
    assert "<b>cluster</b> reachable" in markup


def test_sandbox_ids_and_notes_are_escaped():
    hostile = tile_with(
        id='sb-<script>alert(1)</script>',
        note='boom "quoted" <b>bold</b>',
    )
    markup = render.tile(hostile)
    assert "<script>alert(1)</script>" not in markup
    assert "&lt;script&gt;" in markup
    assert "&quot;quoted&quot;" in markup


def test_grid_counts_the_pool_and_states_the_ceiling():
    report = report_with()
    report.pool_idle = 3
    report.truncated = 2
    markup = render.grid(report)
    # Stats carry a stable data-stat hook; the styling is utilities on top.
    assert 'data-stat="running"' in markup and ">1</span>" in markup
    assert 'data-stat="pending"' in markup
    assert 'data-stat="pool"' in markup and ">0/3</span>" in markup
    assert "2 more not shown" in markup
    # Two cards drawn: the ready one and the pending one.
    assert markup.count('class="card"') == 2


def test_empty_namespace_explains_why_pool_members_are_not_drawn():
    report = inventory.build_report([], claims=[], pods=[], usage={}, now=NOW)
    report.pool_idle = 8
    markup = render.grid(report)
    assert "No claimed sandbox" in markup
    assert "never" in markup and "cards" in markup


def test_collection_errors_are_rendered_rather_than_swallowed():
    report = report_with(errors=["metrics: 404 (Not Found)"])
    assert "metrics: 404 (Not Found)" in render.grid(report)


def test_page_carries_no_outbound_links_and_streams_its_updates():
    page = render.page(report_with())
    # The pane is the pane: no dashboard links in the header.
    assert "Grafana" not in page
    assert "Hubble" not in page
    assert "class=\"links\"" not in page
    # Live updates arrive over SSE; polling is only the fallback.
    assert 'new EventSource("/events")' in page
    assert "startFallback" in page
    assert "VIEWPORT = {w: 1280, h: 600}" in page


def test_expanded_modal_keeps_the_viewport_and_scales_clicks():
    markup = render.expanded(tile_with())
    assert 'class="big"' in markup
    assert "Viewport stays 1280×600" in markup
    assert "divided by the display scale" in markup


def test_the_whole_card_opens_the_modal_and_says_so():
    markup = render.tile(tile_with())
    # Clicking anywhere on the card expands it; the page-driving click lives in
    # the modal, so the tile itself carries no click handler.
    assert 'data-act="expand"' in markup
    assert 'data-act="click"' not in markup
    assert 'role="button"' in markup
    assert "click to expand" in markup


def test_pending_tile_explains_why_there_is_no_frame():
    pending = report_with().tiles[1]  # sb-8c1b, DependenciesNotReady
    markup = render.tile(pending)
    assert "not Ready" in markup
    assert "readiness probe on :9222" in markup


def test_modal_for_a_pending_sandbox_explains_itself_instead_of_a_broken_image():
    pending = report_with().tiles[1]
    markup = render.expanded(pending)
    assert "big-placeholder" in markup
    assert "not Ready" in markup


def test_modal_keeps_the_frame_clickable_for_tapping():
    markup = render.expanded(tile_with())
    assert 'class="big"' in markup
    assert "real mouse press/release" in markup


def _check_scripts(page_html, tmp_path):
    """Run `node --check` over every inline script in a rendered page."""
    node = shutil.which("node")
    if not node:
        pytest.skip("node is not installed")
    scripts = re.findall(r"<script>(.*?)</script>", page_html, re.S)
    assert scripts, "expected at least one inline script"
    for index, body in enumerate(scripts):
        path = tmp_path / f"script-{index}.js"
        path.write_text(body)
        result = subprocess.run([node, "--check", str(path)], capture_output=True, text=True)
        assert result.returncode == 0, f"script {index} does not parse:\n{result.stderr}"


def test_page_javascript_parses(tmp_path):
    """The grid's script must be valid JS.

    A literal newline inside a string once shipped here, and because the whole
    script failed to parse, *every* control on the page died silently: no expand,
    no create, no demo. Nothing in the HTML diff showed it.
    """
    _check_scripts(render.page(report_with()), tmp_path)


def test_compare_page_javascript_parses(tmp_path):
    _check_scripts(render.compare_page(None, {}, allow_raw=True, write_enabled=True), tmp_path)


def test_dialog_offers_compare_and_has_a_container_to_swap():
    markup = render.expanded(tile_with())
    assert 'data-act="compare"' in markup
    assert 'id="dialog-body"' in markup
    assert ">compare<" in markup


def test_compare_columns_fragment_carries_both_arms_and_a_way_back():
    import compare

    class Arm:
        def __init__(self, label, kind, timers, reach):
            self.label = label
            self.kind = kind
            self.timers = timers
            self.reachability = reach
            self.error = None
            self.sandbox = label.lower().replace(" ", "-")

        def total_ms(self):
            return max(self.timers.values())

    run = compare.Run(
        started_at=0.0,
        arms=[
            Arm("THIS SANDBOX", "recorded", {"t_sched_ms": 147, "t_ready_ms": 123}, {"cluster": "blocked"}),
            Arm("BARE SANDBOX", "sandbox", {"t_sched_ms": 800, "t_ready_ms": 3055}, {"cluster": "reachable"}),
        ],
    )
    markup = render.compare_columns(run, allow_raw=True, subject="browser-pool-2fngr")
    assert "THIS SANDBOX" in markup and "BARE SANDBOX" in markup
    assert 'data-act="back" data-id="browser-pool-2fngr"' in markup
    assert markup.count('class="bar"') == 8


def test_compare_columns_explains_an_arm_without_timings():
    import compare

    class Arm:
        label = "THIS SANDBOX"
        kind = "recorded"
        timers = {}
        reachability = {"cluster": "blocked"}
        error = "no recorded timings: this console did not create this sandbox"
        sandbox = "browser-pool-2fngr"

        def total_ms(self):
            return 0

    run = compare.Run(started_at=0.0, arms=[Arm()])
    markup = render.compare_columns(run, allow_raw=True)
    assert "no recorded timings for this sandbox" in markup
    assert "this console did not create this sandbox" in markup


# --------------------------------------------------------------------------- #
# the page a card shows, and changing it without a dialog
# --------------------------------------------------------------------------- #
def test_the_card_shows_the_page_and_offers_to_change_it():
    markup = render.tile(tile_with(url="https://example.com/"))
    assert 'class="url-row"' in markup
    assert 'data-act="url-edit"' in markup
    assert 'data-act="url-form"' in markup
    assert 'name="url"' in markup
    assert "https://example.com/" in markup
    # Editing happens in the card, not in a second window.
    assert "dialog" not in markup


def test_a_card_that_has_not_seen_a_frame_yet_says_the_page_is_unknown():
    markup = render.tile(tile_with(url=None))
    assert "page unknown" in markup
    assert 'value=""' in markup


def test_the_page_is_escaped_into_the_row_and_into_the_input():
    markup = render.tile(tile_with(url='https://example.com/?a=1&b="2"'))
    assert "https://example.com/?a=1&amp;b=&quot;2&quot;" in markup
    assert "<script" not in markup


def test_the_url_editor_is_scoped_to_one_card():
    markup = render.tile(tile_with(url="https://example.com/"))
    assert 'data-act="url-edit" data-id="sb-4f2a"' in markup
    assert 'data-act="url-form" data-id="sb-4f2a"' in markup


def test_structural_markup_is_styled_by_css_not_only_by_utilities():
    """A deleted rule plus a runtime that generates nothing = unstyled page.

    That regression shipped once: the stat row rendered as `pending0` because the
    `.stat*` rules were removed while the markup was rewritten to UnoCSS utility
    classes, and the vendored runtime produced none of them. Structural classes
    must therefore exist in the markup *and* have a rule in the page's CSS.
    """
    page = render.page(report_with())
    css = page.split("</style>")[0]
    pairs = {
        ".app-header": 'class="app-header"',
        ".brand": 'class="brand"',
        ".toolbar": 'class="toolbar"',
        ".separator": 'class="separator"',
        ".stats": 'class="stats"',
        ".stat": 'class="stat"',
        ".stat-label": 'class="stat-label"',
        ".stat-value": 'class="stat-value"',
    }
    for selector, marker in pairs.items():
        assert f"{selector} " in css or f"{selector}{{" in css, f"no CSS rule for {selector}"
        assert marker in page, f"markup lost {marker}"


def test_the_stat_row_renders_label_and_value_as_separate_elements():
    markup = render.grid(report_with())
    # The bug's signature was the two strings touching with nothing between them.
    assert '<span class="stat-label">pool</span><span class="stat-value">0/3</span>' in markup or \
           '<span class="stat-label">pool</span><span class="stat-value">' in markup


def test_the_dialog_offers_both_scroll_directions():
    markup = render.expanded(tile_with())
    assert 'data-act="scroll"' in markup
    assert 'data-dy="-400"' in markup
    assert 'data-dy="400"' in markup
