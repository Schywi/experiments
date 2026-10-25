"""Inventory tests: tile assembly, pool accounting, quantity parsing."""

from datetime import datetime, timezone

import fixtures
import inventory

NOW = datetime(2026, 10, 24, 17, 14, 23, tzinfo=timezone.utc)


def build(sandboxes, claims=None, pods=None, usage=None, max_tiles=12):
    return inventory.build_report(
        sandboxes,
        claims=claims if claims is not None else fixtures.copy_of(fixtures.CLAIM),
        pods=pods if pods is not None else fixtures.copy_of(*fixtures.PODS),
        usage=usage if usage is not None else dict(fixtures.USAGE),
        now=NOW,
        max_tiles=max_tiles,
    )


def test_ready_sandbox_becomes_a_tile_with_status_fields():
    report = build(fixtures.copy_of(fixtures.READY_SANDBOX))
    assert len(report.tiles) == 1
    tile = report.tiles[0]
    assert tile.id == "sb-4f2a"
    assert tile.phase == "ready"
    assert tile.pod_ip == "10.42.0.36"
    assert tile.node == "fedora"
    assert tile.template == "browser-tpl"
    assert tile.claim == "demo-3"
    assert tile.launch_type == "warm"
    # 17:12:03 -> 17:14:23
    assert tile.age_s == 140
    assert tile.mem_working_set_bytes == 262144000
    assert tile.cpu_millicores == 42
    assert report.running == 1
    assert report.pending == 0


def test_unclaimed_pool_member_is_counted_but_not_drawn():
    sandboxes = fixtures.copy_of(fixtures.READY_SANDBOX, fixtures.POOL_MEMBER)
    report = build(sandboxes, claims=fixtures.copy_of(fixtures.CLAIM))
    assert [tile.id for tile in report.tiles] == ["sb-4f2a"]
    assert report.pool_idle == 1
    assert report.pool_total == 1
    # The fixture member is Ready=True, so it counts as warmth and not as a
    # broken pool.
    assert report.pool_ready == 1


def test_adopted_pool_member_is_drawn_once_the_label_is_gone():
    sandboxes = fixtures.copy_of(fixtures.ADOPTED_POOL_SANDBOX)
    claims = [{"metadata": {"name": "demo-9"}, "status": {"sandbox": {"name": "browser-pool-7mqd9"}}}]
    report = build(sandboxes, claims=claims)
    assert [tile.id for tile in report.tiles] == ["browser-pool-7mqd9"]
    assert report.tiles[0].claim == "demo-9"
    assert report.pool_idle == 0
    assert report.tiles[0].mem_working_set_bytes == 10485760


def test_pending_sandbox_reports_its_condition_as_phase():
    report = build(fixtures.copy_of(fixtures.PENDING_SANDBOX), claims=[])
    tile = report.tiles[0]
    assert tile.phase == "pending"
    assert tile.ready is False
    assert report.pending == 1
    assert report.running == 0


def test_missing_metrics_leave_numbers_empty_instead_of_zero():
    report = build(fixtures.copy_of(fixtures.READY_SANDBOX), usage={})
    tile = report.tiles[0]
    assert tile.mem_working_set_bytes is None
    assert tile.cpu_millicores is None
    assert report.mem_working_set_bytes == 0


def test_tiles_are_sorted_and_capped_with_the_remainder_counted():
    sandboxes = []
    for index in range(5):
        item = fixtures.copy_of(fixtures.READY_SANDBOX)[0]
        item["metadata"]["name"] = f"sb-{5 - index}"
        item["status"]["selector"] = "agents.x-k8s.io/sandbox-name-hash=abc123"
        sandboxes.append(item)
    report = build(sandboxes, max_tiles=3)
    assert [tile.id for tile in report.tiles] == ["sb-1", "sb-2", "sb-3"]
    assert report.truncated == 2
    # Counters still describe every sandbox, not just the drawn ones.
    assert report.running == 5


def test_unknown_ready_reason_is_visible_rather_than_hidden():
    sandbox = fixtures.copy_of(fixtures.PENDING_SANDBOX)[0]
    sandbox["status"]["conditions"] = [
        {"type": "Ready", "status": "False", "reason": "SomethingNewUpstream"}
    ]
    report = build([sandbox], claims=[])
    assert report.tiles[0].phase == "somethingnewupstream"


def test_parse_quantity_handles_binary_and_decimal_suffixes():
    assert inventory.parse_quantity("42m") == 0.042
    assert inventory.parse_quantity("256000Ki") == 262144000
    assert inventory.parse_quantity("1.5Gi") == 1610612736
    assert inventory.parse_quantity("2M") == 2000000
    assert inventory.parse_quantity("7") == 7
    assert inventory.parse_quantity(None) is None
    assert inventory.parse_quantity("nonsense") is None
    assert inventory.parse_quantity(True) is None


def test_cpu_and_memory_helpers_keep_the_units_each_field_needs():
    # `42m` is 0.042 cores; as millicores it is the 42 the tile shows.
    assert inventory.cpu_millicores("42m") == 42
    assert inventory.cpu_millicores("1.5") == 1500
    assert inventory.memory_bytes("256000Ki") == 262144000
    assert inventory.memory_bytes("250Mi") == 262144000
    assert inventory.cpu_millicores(None) is None


def test_selector_field_names_from_both_api_versions_are_accepted():
    current = fixtures.copy_of(fixtures.READY_SANDBOX)[0]
    current["status"]["labelSelector"] = current["status"].pop("selector")
    assert inventory.name_hash(current["status"]["labelSelector"]) == "abc123"
    report = build([current], claims=[])
    # Same pod met either way, so memory and CPU are attached.
    assert report.tiles[0].mem_working_set_bytes == 262144000
    assert report.tiles[0].pod_name == "sb-4f2a-pod"


def test_pod_is_matched_by_ip_when_the_sandbox_has_no_selector():
    sandbox = fixtures.copy_of(fixtures.READY_SANDBOX)[0]
    sandbox["status"].pop("selector")
    report = build([sandbox], claims=[])
    tile = report.tiles[0]
    assert tile.pod_name == "sb-4f2a-pod"
    assert tile.cpu_millicores == 42


def test_name_hash_is_only_read_from_the_expected_key():
    assert inventory.name_hash("agents.x-k8s.io/sandbox-name-hash=abc") == "abc"
    assert inventory.name_hash("other=abc") is None
    assert inventory.name_hash(None) is None


def test_pod_ip_falls_back_to_the_pod_when_status_has_none():
    sandbox = fixtures.copy_of(fixtures.READY_SANDBOX)[0]
    sandbox["status"].pop("podIPs")
    sandbox["status"].pop("nodeName")
    report = build([sandbox], claims=[])
    assert report.tiles[0].pod_ip == "10.42.0.36"
    assert report.tiles[0].node == "fedora"


def test_shutdown_time_is_surfaced():
    sandbox = fixtures.copy_of(fixtures.READY_SANDBOX)[0]
    sandbox["spec"]["shutdownTime"] = "2026-10-24T18:20:00Z"
    report = build([sandbox], claims=[])
    assert report.tiles[0].shutdown_at == "2026-10-24T18:20:00Z"


def test_build_usage_sums_the_containers_of_a_podmetrics_item():
    # The real shape: usage lives under containers[], not at the top level.
    usage = inventory.build_usage(
        [
            {
                "metadata": {"name": "sb-1"},
                "containers": [
                    {"name": "browser", "usage": {"cpu": "1500m", "memory": "131072Ki"}},
                    {"name": "bridge", "usage": {"cpu": "10m", "memory": "1024Ki"}},
                ],
            },
            {"metadata": {}, "containers": [{"name": "x", "usage": {"cpu": "1"}}]},
        ]
    )
    assert usage["sb-1"] == {"cpu_millicores": 1510, "mem_working_set_bytes": 135266304}
    assert len(usage) == 1


def test_build_usage_still_accepts_a_flattened_item():
    usage = inventory.build_usage(
        [{"metadata": {"name": "sb-2"}, "usage": {"cpu": "42m", "memory": "250Mi"}}]
    )
    assert usage["sb-2"] == {"cpu_millicores": 42, "mem_working_set_bytes": 262144000}


def test_pool_members_that_are_not_ready_are_not_counted_as_warmth():
    member = fixtures.copy_of(fixtures.POOL_MEMBER)[0]
    member["status"]["conditions"] = [
        {"type": "Ready", "status": "False", "reason": "DependenciesNotReady"}
    ]
    report = build([member], claims=[])
    assert report.pool_idle == 1
    assert report.pool_ready == 0


def test_pod_projection_uses_the_same_camel_case_as_the_crd_dicts():
    """A V1Pod's to_dict() is snake_case; the badge reads camelCase.

    Getting this wrong made a hardened pod report `sa token: mounted` and
    `dns: clusterfirst` -- the unhardened defaults -- because `spec` had
    `automount_service_account_token` instead of the key the badge looks for.
    """
    from types import SimpleNamespace

    import kube

    pod = SimpleNamespace(
        metadata=SimpleNamespace(name="sb-1-pod", labels={"agents.x-k8s.io/sandbox-name-hash": "abc"}),
        spec=SimpleNamespace(
            node_name="fedora",
            dns_policy="None",
            dns_config=SimpleNamespace(nameservers=["8.8.8.8", "1.1.1.1"]),
            automount_service_account_token=False,
        ),
        status=SimpleNamespace(pod_ip="10.42.0.201"),
    )
    summary = kube._pod_summary(pod)
    assert summary["status"]["podIP"] == "10.42.0.201"
    assert summary["metadata"]["labels"]["agents.x-k8s.io/sandbox-name-hash"] == "abc"
    assert summary["spec"]["automountServiceAccountToken"] is False
    assert summary["spec"]["dnsPolicy"] == "None"

    import probes

    claims = probes.spec_claims(summary)
    assert claims["sa_token"] == "absent"
    assert claims["cluster_dns"] == "none:8.8.8.8,1.1.1.1"
