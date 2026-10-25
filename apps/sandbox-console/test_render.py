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
    # Stats are label/value pairs so the numbers sit on their own type scale.
    assert '<span class="stat-label">running</span><span class="stat-value">1</span>' in markup
    assert '<span class="stat-label">pending</span><span class="stat-value">1</span>' in markup
    assert '<span class="stat-label">pool</span><span class="stat-value">0/3</span>' in markup
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
