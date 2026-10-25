"""Lifecycle tests: what create/delete actually send, and how the timers stamp.

The Kubernetes client is replaced with a recorder, so these assert the request
bodies rather than a cluster's reaction to them.
"""

import asyncio
import time

import lifecycle
from kubernetes import client


class Recorder:
    def __init__(self):
        self.created = []
        self.deleted = []
        self.gets = []
        self.lists = []
        self.claims = []
        self.sandboxes = {}

    # --- writes -------------------------------------------------------------
    def create_namespaced_custom_object(self, group, version, namespace, plural, body):
        self.created.append({"group": group, "version": version, "namespace": namespace, "plural": plural, "body": body})
        return body

    def delete_namespaced_custom_object(self, group, version, namespace, plural, name, **kwargs):
        self.deleted.append({"plural": plural, "name": name, "kwargs": kwargs})

    # --- reads --------------------------------------------------------------
    def get_namespaced_custom_object(self, group, version, namespace, plural, name):
        self.gets.append((plural, name))
        if plural == "sandboxclaims":
            item = self.claims_by_name(name)
            if item is None:
                raise client.exceptions.ApiException(status=404)
            return item
        item = self.sandboxes.get(name)
        if item is None:
            raise client.exceptions.ApiException(status=404)
        return item

    def list_namespaced_custom_object(self, group, version, namespace, plural, **kwargs):
        self.lists.append({"plural": plural, "kwargs": kwargs})
        if plural == "sandboxclaims":
            return {"items": self.claims}
        return {"items": []}

    def claims_by_name(self, name):
        for claim in self.claims:
            if (claim.get("metadata") or {}).get("name") == name:
                return claim
        return None


def run(coro):
    return asyncio.run(coro)


def ready_sandbox(name, ready=True, scheduled=True):
    conditions = []
    if scheduled:
        conditions.append({"type": "PodScheduled", "status": "True", "reason": "PodScheduled"})
    conditions.append(
        {
            "type": "Ready",
            "status": "True" if ready else "False",
            "reason": "DependenciesReady" if ready else "DependenciesNotReady",
        }
    )
    return {"metadata": {"name": name}, "status": {"conditions": conditions}}


def test_claim_creation_marks_it_managed_and_points_at_the_pool(monkeypatch):
    recorder = Recorder()
    monkeypatch.setattr(lifecycle, "_custom", lambda: recorder)

    created = lifecycle.create_claim("sandbox-demo", "browser-pool", name="claim-test")

    assert created.kind == "claim"
    assert created.name == "claim-test"
    body = recorder.created[0]["body"]
    assert body["kind"] == "SandboxClaim"
    assert body["metadata"]["labels"] == {"sandbox-console": "managed"}
    assert body["spec"] == {"warmPoolRef": {"name": "browser-pool"}}
    assert recorder.created[0]["plural"] == "sandboxclaims"


def test_bare_sandbox_creation_is_unhardened_by_construction(monkeypatch):
    recorder = Recorder()
    monkeypatch.setattr(lifecycle, "_custom", lambda: recorder)

    lifecycle.create_sandbox(
        "sandbox-demo",
        image="127.0.0.1:5000/browser-sandbox:local",
        target_url="https://example.com/",
        cdp_port=9222,
        name="bare-test",
    )

    spec = recorder.created[0]["body"]["spec"]["podTemplate"]["spec"]
    # The whole point of the bare column: no NetworkPolicy fields, no dnsPolicy
    # override, no automountServiceAccountToken setting to inherit from.
    assert "dnsPolicy" not in spec
    assert "automountServiceAccountToken" not in spec
    assert recorder.created[0]["body"]["metadata"]["labels"] == {"sandbox-console": "managed"}
    container = spec["containers"][0]
    assert container["ports"][0]["containerPort"] == 9222
    assert {"name": "TARGET_URL", "value": "https://example.com/"} in container["env"]


def test_delete_prefers_the_claim_that_owns_the_sandbox(monkeypatch):
    recorder = Recorder()
    recorder.claims = [
        {"metadata": {"name": "demo-2"}, "status": {"sandbox": {"name": "browser-pool-2fngr"}}}
    ]
    monkeypatch.setattr(lifecycle, "_custom", lambda: recorder)

    removed = lifecycle.delete_any("sandbox-demo", "browser-pool-2fngr")

    assert removed == "claim/demo-2"
    assert recorder.deleted[0]["plural"] == "sandboxclaims"
    assert recorder.deleted[0]["name"] == "demo-2"


def test_delete_falls_back_to_the_sandbox_when_no_claim_owns_it(monkeypatch):
    recorder = Recorder()
    recorder.claims = []
    monkeypatch.setattr(lifecycle, "_custom", lambda: recorder)

    removed = lifecycle.delete_any("sandbox-demo", "bare-1")

    assert removed == "sandbox/bare-1"
    assert recorder.deleted[0]["plural"] == "sandboxes"


def test_wait_for_sandbox_stamps_scheduled_then_ready(monkeypatch):
    recorder = Recorder()
    recorder.claims = [{"metadata": {"name": "c1"}, "status": {"sandbox": {"name": "sb-1"}}}]
    recorder.sandboxes = {"sb-1": ready_sandbox("sb-1")}
    monkeypatch.setattr(lifecycle, "_custom", lambda: recorder)

    created = lifecycle.Created(kind="claim", name="c1", namespace="sandbox-demo", started_at=time.monotonic())
    name = run(lifecycle.wait_for_sandbox(created, deadline=time.monotonic() + 5))

    assert name == "sb-1"
    assert created.sandbox == "sb-1"
    assert "t_sched_ms" in created.timers
    assert created.error is None


def test_wait_for_sandbox_reports_a_terminal_failure_without_hanging(monkeypatch):
    recorder = Recorder()
    recorder.claims = []
    recorder.sandboxes = {
        "bare-1": {
            "metadata": {"name": "bare-1"},
            "status": {"conditions": [{"type": "Ready", "status": "False", "reason": "PodFailed"}]},
        }
    }
    monkeypatch.setattr(lifecycle, "_custom", lambda: recorder)

    created = lifecycle.Created(kind="sandbox", name="bare-1", namespace="sandbox-demo", started_at=time.monotonic())
    created.sandbox = "bare-1"
    run(lifecycle.wait_for_sandbox(created, deadline=time.monotonic() + 5))

    assert created.error == "PodFailed"


def test_generated_names_are_dns_labels_short_enough_for_a_service():
    name = lifecycle.safe_name("claim")
    assert lifecycle.valid_name(name)
    assert len(name) < 30
    assert not lifecycle.valid_name("Bad_Name")
    assert not lifecycle.valid_name("")


def test_managed_claims_use_the_console_label(monkeypatch):
    recorder = Recorder()
    recorder.claims = [{"metadata": {"name": "demo-2"}}]
    monkeypatch.setattr(lifecycle, "_custom", lambda: recorder)

    claims = lifecycle.managed_claims("sandbox-demo")

    assert [c["metadata"]["name"] for c in claims] == ["demo-2"]
    assert recorder.lists[0]["kwargs"]["label_selector"] == "sandbox-console=managed"
