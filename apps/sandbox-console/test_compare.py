"""Compare-mode tests: the timing, the p50, and the shared bar scale.

The cluster is replaced: `lifecycle` functions are monkeypatched so the two arms
finish with known timers, and the CDP client is a stub. What is asserted is the
shape of the report and the fact that both bars use one scale.
"""

import asyncio
import time

import compare
import lifecycle
import render


def run(coro):
    return asyncio.run(coro)


class StubCDP:
    async def list_targets(self, sandbox_id):
        return [{"type": "page"}]

    async def screenshot(self, sandbox_id):
        return b"x" * 6000

    async def evaluate(self, sandbox_id, expression):
        return '{"cluster": {"outcome": "timeout", "ms": 4001}}'


def install(monkeypatch, claim_ms=1553, bare_ms=43200, bare=True):
    """Fake the two creations and their timers."""
    def fake_claim(namespace, pool, name=None):
        return lifecycle.Created("claim", "claim-1", namespace, time.monotonic())

    def fake_sandbox(namespace, image, target_url, cdp_port, name=None, **kwargs):
        if not bare:
            raise RuntimeError("should not be called")
        return lifecycle.Created("sandbox", "bare-1", namespace, time.monotonic())

    async def fake_wait(created, deadline):
        # Stamp the timers directly: the real waiter polls the API.
        created.sandbox = created.name
        created.timers["t_sched_ms"] = 200
        created.timers["t_ready_ms"] = claim_ms if created.kind == "claim" else bare_ms
        time.sleep(0)
        return created.sandbox

    async def fake_cdp(client, created, deadline):
        created.timers["t_cdp_ms"] = created.timers["t_ready_ms"] + 60
        return True

    async def fake_paint(client, created, deadline):
        created.timers["t_paint_ms"] = created.timers["t_ready_ms"] + 170
        return True

    monkeypatch.setattr(lifecycle, "create_claim", fake_claim)
    monkeypatch.setattr(lifecycle, "create_sandbox", fake_sandbox)
    monkeypatch.setattr(lifecycle, "wait_for_sandbox", fake_wait)
    monkeypatch.setattr(lifecycle, "wait_for_cdp", fake_cdp)
    monkeypatch.setattr(lifecycle, "wait_for_paint", fake_paint)
    compare.HISTORY.clear()


def test_compare_times_both_arms_and_records_all_four_timers(monkeypatch):
    install(monkeypatch)
    run_ = run(compare.run_compare(StubCDP(), allow_raw=True, timeout=5))

    assert [arm.label for arm in run_.arms] == ["CLAIM (warm pool)", "BARE SANDBOX"]
    claim_arm, bare_arm = run_.arms
    for arm in (claim_arm, bare_arm):
        assert set(arm.timers) == {"t_sched_ms", "t_ready_ms", "t_cdp_ms", "t_paint_ms"}
    assert claim_arm.timers["t_ready_ms"] == 1553
    assert bare_arm.timers["t_ready_ms"] == 43200
    assert claim_arm.error is None and bare_arm.error is None


def test_compare_without_allow_raw_runs_one_arm_and_says_so(monkeypatch):
    install(monkeypatch, bare=False)
    run_ = run(compare.run_compare(StubCDP(), allow_raw=False, timeout=5))

    assert [arm.label for arm in run_.arms] == ["CLAIM (warm pool)"]
    assert "allowRawSandbox is off" in (run_.note or "")


def test_history_keeps_three_runs_and_reports_p50(monkeypatch):
    install(monkeypatch, claim_ms=1000)
    for _ in range(4):
        run(compare.run_compare(StubCDP(), allow_raw=False, timeout=5))
    assert len(compare.HISTORY) == 3
    medians = compare.history_medians()
    assert medians["CLAIM (warm pool)"]["t_ready_ms"] == 1000


def test_p50_of_an_even_count_is_the_middle_average():
    assert compare.p50([1, 2, 3]) == 2
    assert compare.p50([1, 2, 3, 4]) == 2
    assert compare.p50([]) is None
    assert compare.p50([None, 5]) == 5


def test_the_two_columns_share_one_bar_scale(monkeypatch):
    install(monkeypatch, claim_ms=1553, bare_ms=43200)
    run_ = run(compare.run_compare(StubCDP(), allow_raw=True, timeout=5))
    page = render.compare_page(run_, compare.history_medians(), allow_raw=True, write_enabled=True)

    # The claim's last timer is the shortest bar in the picture; the bare arm's is
    # the longest. A shared scale means the short one must be much narrower.
    assert 'width:100%' in page
    assert page.count('class="bar"') >= 8
    widths = [
        int(chunk.split("width:")[1].split("%")[0])
        for chunk in page.split('class="bar"')[1:]
        if "width:" in chunk
    ]
    assert max(widths) == 100
    assert min(widths) < 10


def test_compare_page_without_a_run_explains_what_one_press_does():
    page = render.compare_page(None, {}, allow_raw=False, write_enabled=True)
    assert "No comparison has run yet" in page
    assert "allowRawSandbox is off" in page
    assert "run compare" in page


def test_compare_page_in_read_only_mode_has_no_run_button():
    page = render.compare_page(None, {}, allow_raw=False, write_enabled=False)
    assert "run compare" not in page
    assert "read-only" in page


def test_compare_probes_both_arms_after_timing(monkeypatch):
    install(monkeypatch)
    probed = []

    async def fake_probe(arm, client, timeout_ms):
        probed.append(arm.label)
        return {"cluster": "blocked" if arm.kind == "claim" else "reachable"}

    monkeypatch.setattr(compare, "probe_arm", fake_probe)
    run_ = run(compare.run_compare(StubCDP(), allow_raw=True, timeout=5))

    assert probed == ["CLAIM (warm pool)", "BARE SANDBOX"]
    assert run_.arms[0].reachability == {"cluster": "blocked"}
    assert run_.arms[1].reachability == {"cluster": "reachable"}


def test_wait_for_sandbox_stamps_the_ready_timer(monkeypatch):
    import time as _time

    import lifecycle

    sandbox = {
        "metadata": {"name": "sb-1"},
        "status": {"conditions": [{"type": "Ready", "status": "True", "reason": "DependenciesReady"}]},
    }
    monkeypatch.setattr(lifecycle, "get_sandbox", lambda namespace, name: sandbox)
    created = lifecycle.Created("sandbox", "sb-1", "sandbox-demo", _time.monotonic())
    created.sandbox = "sb-1"
    run(lifecycle.wait_for_sandbox(created, deadline=_time.monotonic() + 5))
    assert "t_ready_ms" in created.timers
    assert "t_sched_ms" in created.timers
